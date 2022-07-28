package sysload

import (
	"runtime"
	"time"

	"github.com/frostbyte73/go-throttle"
	"github.com/mackerelio/go-osstat/cpu"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/atomic"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/protocol/logger"
)

var (
	idleCPUs        atomic.Float64
	pendingCPUs     atomic.Float64
	numCPUs         = float64(runtime.NumCPU())
	warningThrottle = throttle.New(time.Minute)

	promCPULoad prometheus.Gauge
)

func Init(conf *config.Config, close chan struct{}, isAvailable func() float64) error {
	promCPULoad = prometheus.NewGauge(prometheus.GaugeOpts{
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

	prometheus.MustRegister(promCPULoad)
	prometheus.MustRegister(promNodeAvailable)

	go monitorCPULoad(close)
	return nil
}

func monitorCPULoad(close chan struct{}) {
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
			idleCPUs.Store(numCPUs * idlePercent)
			promCPULoad.Set(1 - idlePercent)

			if idlePercent < 0.1 {
				warningThrottle(func() { logger.Infow("high cpu load", "load", 100-idlePercent) })
			}

			prev = next
		}
	}
}

func GetCPULoad() float64 {
	return (numCPUs - idleCPUs.Load()) / numCPUs * 100
}
