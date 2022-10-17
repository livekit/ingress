package stats

import (
	"errors"
	"fmt"
	"runtime"
	"sort"
	"time"

	"github.com/frostbyte73/go-throttle"
	"github.com/mackerelio/go-osstat/cpu"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/atomic"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

type Monitor struct {
	cpuCostConfig config.CPUCostConfig
	maxCost       float64

	promCPULoad  prometheus.Gauge
	requestGauge *prometheus.GaugeVec

	idleCPUs        atomic.Float64
	pendingCPUs     atomic.Float64
	numCPUs         float64
	warningThrottle func(func())
}

func NewMonitor() *Monitor {
	return &Monitor{
		numCPUs:         float64(runtime.NumCPU()),
		warningThrottle: throttle.New(time.Minute),
	}
}

func (m *Monitor) Start(conf *config.Config, close chan struct{}, isAvailable func() float64) error {
	if err := m.checkCPUConfig(conf.CPUCost); err != nil {
		return err
	}

	m.promCPULoad = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "node",
		Name:        "cpu_load",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID, "node_type": "INGRESS"},
	})
	promNodeAvailable := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "ingress",
		Name:        "available",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID},
	}, isAvailable)
	m.requestGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "ingress",
		Name:        "requests",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID},
	}, []string{"type"})

	prometheus.MustRegister(m.promCPULoad, promNodeAvailable, m.requestGauge)

	go m.monitorCPULoad(close)
	return nil
}

func (m *Monitor) checkCPUConfig(costConfig config.CPUCostConfig) error {
	if costConfig.RTMPCpuCost < 1 {
		logger.Warnw("rtmp input requirement too low", nil,
			"config value", costConfig.RTMPCpuCost,
			"minimum value", 1,
			"recommended value", 2,
		)
	}

	requirements := []float64{
		costConfig.RTMPCpuCost,
	}
	sort.Float64s(requirements)
	m.maxCost = requirements[len(requirements)-1]

	recommendedMinimum := m.maxCost
	if recommendedMinimum < 3 {
		recommendedMinimum = 3
	}

	if m.numCPUs < requirements[0] {
		logger.Errorw("not enough cpu", nil,
			"minimum cpu", requirements[0],
			"recommended", recommendedMinimum,
			"available", m.numCPUs,
		)
		return errors.New("not enough cpu")
	}

	if m.numCPUs < m.maxCost {
		logger.Errorw("not enough cpu for some ingress types", nil,
			"minimum cpu", m.maxCost,
			"recommended", recommendedMinimum,
			"available", m.numCPUs,
		)
	}

	logger.Infow(fmt.Sprintf("available CPU cores: %f max cost: %f", m.numCPUs, m.maxCost))

	return nil
}

func (m *Monitor) monitorCPULoad(close chan struct{}) {
	prev, _ := cpu.Get()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-close:
			return
		case <-ticker.C:
			next, _ := cpu.Get()
			idlePercent := float64(next.Idle-prev.Idle) / float64(next.Total-prev.Total)
			m.idleCPUs.Store(m.numCPUs * idlePercent)
			m.promCPULoad.Set(1 - idlePercent)

			if idlePercent < 0.1 {
				m.warningThrottle(func() { logger.Infow("high cpu load", "load", 1-idlePercent) })
			}

			prev = next
		}
	}
}

func (m *Monitor) GetCPULoad() float64 {
	return (m.numCPUs - m.idleCPUs.Load()) / m.numCPUs * 100
}

func (m *Monitor) CanAcceptIngress() bool {
	available := m.idleCPUs.Load() - m.pendingCPUs.Load()

	return available > 1.2*m.maxCost
}

func (m *Monitor) AcceptIngress(info *livekit.IngressInfo) bool {
	var cpuHold float64
	var accept bool
	available := m.idleCPUs.Load() - m.pendingCPUs.Load()

	switch info.InputType {
	case livekit.IngressInput_RTMP_INPUT:
		accept = available > m.cpuCostConfig.RTMPCpuCost
		cpuHold = m.cpuCostConfig.RTMPCpuCost
	default:
		logger.Errorw("unsupported request type", errors.New("invalid parameter"))
	}

	if accept {
		m.pendingCPUs.Add(cpuHold)
		time.AfterFunc(time.Second, func() { m.pendingCPUs.Sub(cpuHold) })
	}

	logger.Debugw("cpu request", "accepted", accept, "availableCPUs", available, "numCPUs", runtime.NumCPU())
	return accept
}

func (m *Monitor) IngressStarted(info *livekit.IngressInfo) {
	switch info.InputType {
	case livekit.IngressInput_RTMP_INPUT:
		m.requestGauge.With(prometheus.Labels{"type": "rtmp"}).Add(1)
	}
}

func (m *Monitor) IngressEnded(info *livekit.IngressInfo) {
	switch info.InputType {
	case livekit.IngressInput_RTMP_INPUT:
		m.requestGauge.With(prometheus.Labels{"type": "rtmp"}).Sub(1)
	}
}
