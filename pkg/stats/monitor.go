// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package stats

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/atomic"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils/hwstats"
)

const (
	defaultMinIdle float64 = 0.3 // Target at least 30% idle CPU
)

type Monitor struct {
	costConfigLock sync.Mutex
	cpuCostConfig  config.CPUCostConfig
	maxCost        float64

	promCPULoad       prometheus.Gauge
	requestGauge      *prometheus.GaugeVec
	promNodeAvailable prometheus.GaugeFunc

	cpuStats *hwstats.CPUStats

	pendingCPUs atomic.Float64

	started  core.Fuse
	shutdown core.Fuse
}

func NewMonitor() *Monitor {
	return &Monitor{}
}

func (m *Monitor) Start(conf *config.Config) error {
	cpuStats, err := hwstats.NewCPUStats(func(idle float64) {
		m.promCPULoad.Set(1 - idle/float64(m.cpuStats.NumCPU()))
	})
	if err != nil {
		return err
	}

	m.costConfigLock.Lock()
	m.cpuStats = cpuStats
	m.cpuCostConfig = conf.CPUCost

	if err := m.checkCPUConfig(); err != nil {
		m.costConfigLock.Unlock()
		return err
	}
	m.costConfigLock.Unlock()

	m.promCPULoad = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "node",
		Name:        "cpu_load",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID, "node_type": "INGRESS"},
	})
	m.promNodeAvailable = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "ingress",
		Name:        "available",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID},
	}, func() float64 {
		c := m.CanAccept()
		if c {
			return 1
		}
		return 0
	})
	m.requestGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace:   "livekit",
		Subsystem:   "ingress",
		Name:        "requests",
		ConstLabels: prometheus.Labels{"node_id": conf.NodeID},
	}, []string{"type", "transcoding"})

	prometheus.MustRegister(m.promCPULoad, m.promNodeAvailable, m.requestGauge)

	m.started.Break()

	return nil
}

func (m *Monitor) UpdateCostConfig(cpuCostConfig *config.CPUCostConfig) error {
	m.costConfigLock.Lock()
	defer m.costConfigLock.Unlock()

	// No change
	if m.cpuCostConfig == *cpuCostConfig {
		return nil
	}

	// Update config, but return an error if validation fails
	m.cpuCostConfig = *cpuCostConfig

	return m.checkCPUConfig()
}

// Server is shutting down, but may stay up for some time for draining
func (m *Monitor) Shutdown() {
	m.shutdown.Break()
}

// Stop the monitor before server termination
func (m *Monitor) Stop() {
	if m.cpuStats != nil {
		m.cpuStats.Stop()
	}

	prometheus.Unregister(m.promCPULoad)
	prometheus.Unregister(m.requestGauge)
	prometheus.Unregister(m.promNodeAvailable)
}

func (m *Monitor) checkCPUConfig() error {
	// Not started
	if m.cpuStats == nil {
		return nil
	}

	if m.cpuCostConfig.MinIdleRatio <= 0 {
		m.cpuCostConfig.MinIdleRatio = defaultMinIdle
	}

	if m.cpuCostConfig.RTMPCpuCost < 1 {
		logger.Warnw("rtmp input requirement too low", nil,
			"config value", m.cpuCostConfig.RTMPCpuCost,
			"minimum value", 1,
			"recommended value", 2,
		)
	}

	if m.cpuCostConfig.WHIPCpuCost < 1 {
		logger.Warnw("whip input requirement too low", nil,
			"config value", m.cpuCostConfig.WHIPCpuCost,
			"minimum value", 1,
			"recommended value", 2,
		)
	}

	if m.cpuCostConfig.WHIPBypassTranscodingCpuCost < 0.05 {
		logger.Warnw("whip input with transcoding bypassed requirement too low", nil,
			"config value", m.cpuCostConfig.WHIPCpuCost,
			"minimum value", 0.05,
			"recommended value", 0.1,
		)
	}

	if m.cpuCostConfig.URLCpuCost < 1 {
		logger.Warnw("url input requirement too low", nil,
			"config value", m.cpuCostConfig.URLCpuCost,
			"minimum value", 1,
			"recommended value", 2,
		)
	}

	requirements := []float64{
		m.cpuCostConfig.RTMPCpuCost,
		m.cpuCostConfig.WHIPCpuCost,
		m.cpuCostConfig.WHIPBypassTranscodingCpuCost,
		m.cpuCostConfig.URLCpuCost,
	}
	sort.Float64s(requirements)
	m.maxCost = requirements[len(requirements)-1]

	recommendedMinimum := m.maxCost
	if recommendedMinimum < 3 {
		recommendedMinimum = 3
	}

	if float64(m.cpuStats.NumCPU()) < requirements[0] {
		logger.Errorw("not enough cpu", nil,
			"minimum cpu", requirements[0],
			"recommended", recommendedMinimum,
			"available", float64(m.cpuStats.NumCPU()),
		)
		return errors.ErrServerCapacityExceeded
	}

	if float64(m.cpuStats.NumCPU()) < m.maxCost {
		logger.Errorw("not enough cpu for some ingress types", nil,
			"minimum cpu", m.maxCost,
			"recommended", recommendedMinimum,
			"available", m.cpuStats.NumCPU(),
		)
	}

	logger.Infow(fmt.Sprintf("available CPU cores: %f max cost: %f", m.cpuStats.NumCPU(), m.maxCost))

	return nil
}

func (m *Monitor) GetAvailableCPU() float64 {
	return m.getAvailable(m.cpuCostConfig.MinIdleRatio)
}

func (m *Monitor) CanAccept() bool {
	if !m.started.IsBroken() || m.shutdown.IsBroken() {
		return false
	}

	m.costConfigLock.Lock()
	defer m.costConfigLock.Unlock()

	return m.getAvailable(m.cpuCostConfig.MinIdleRatio) > m.maxCost
}

func (m *Monitor) canAcceptIngress(info *livekit.IngressInfo, alreadyCommitted bool) (bool, float64, float64) {
	if !m.started.IsBroken() || m.shutdown.IsBroken() {
		return false, 0, 0
	}

	var cpuHold float64
	var accept bool

	m.costConfigLock.Lock()
	defer m.costConfigLock.Unlock()

	minIdle := m.cpuCostConfig.MinIdleRatio
	if alreadyCommitted {
		minIdle /= 2
	}

	available := m.getAvailable(minIdle)

	switch info.InputType {
	case livekit.IngressInput_RTMP_INPUT:
		accept = available > m.cpuCostConfig.RTMPCpuCost
		cpuHold = m.cpuCostConfig.RTMPCpuCost
	case livekit.IngressInput_WHIP_INPUT:
		if !*info.EnableTranscoding {
			accept = available > m.cpuCostConfig.WHIPBypassTranscodingCpuCost
			cpuHold = m.cpuCostConfig.WHIPBypassTranscodingCpuCost
		} else {
			accept = available > m.cpuCostConfig.WHIPCpuCost
			cpuHold = m.cpuCostConfig.WHIPCpuCost
		}
	case livekit.IngressInput_URL_INPUT:
		accept = available > m.cpuCostConfig.URLCpuCost
		cpuHold = m.cpuCostConfig.URLCpuCost

	default:
		logger.Errorw("unsupported request type", errors.New("invalid parameter"))
	}

	logger.Debugw("checking if request can be handled", "inputType", info.InputType, "accept", accept, "available", available, "cpuHold", cpuHold, "idle", m.cpuStats.GetCPUIdle(), "pending", m.pendingCPUs.Load())

	return accept, cpuHold, available
}

func (m *Monitor) CanAcceptIngress(info *livekit.IngressInfo) bool {
	params.UpdateTranscodingEnabled(info)

	accept, _, _ := m.canAcceptIngress(info, false)

	return accept
}

func (m *Monitor) AcceptIngress(info *livekit.IngressInfo) bool {
	accept, cpuHold, available := m.canAcceptIngress(info, true)

	if accept {
		m.pendingCPUs.Add(cpuHold)
		time.AfterFunc(time.Second, func() { m.pendingCPUs.Sub(cpuHold) })
	}

	logger.Debugw("cpu request", "accepted", accept, "availableCPUs", available, "numCPUs", m.cpuStats.NumCPU())
	return accept
}

func (m *Monitor) IngressStarted(info *livekit.IngressInfo) {
	switch info.InputType {
	case livekit.IngressInput_RTMP_INPUT:
		m.requestGauge.With(prometheus.Labels{"type": "rtmp", "transcoding": fmt.Sprintf("%v", *info.EnableTranscoding)}).Add(1)
	case livekit.IngressInput_WHIP_INPUT:
		m.requestGauge.With(prometheus.Labels{"type": "whip", "transcoding": fmt.Sprintf("%v", *info.EnableTranscoding)}).Add(1)
	case livekit.IngressInput_URL_INPUT:
		m.requestGauge.With(prometheus.Labels{"type": "url", "transcoding": fmt.Sprintf("%v", *info.EnableTranscoding)}).Add(1)

	}
}

func (m *Monitor) IngressEnded(info *livekit.IngressInfo) {
	switch info.InputType {
	case livekit.IngressInput_RTMP_INPUT:
		m.requestGauge.With(prometheus.Labels{"type": "rtmp", "transcoding": fmt.Sprintf("%v", *info.EnableTranscoding)}).Sub(1)
	case livekit.IngressInput_WHIP_INPUT:
		m.requestGauge.With(prometheus.Labels{"type": "whip", "transcoding": fmt.Sprintf("%v", *info.EnableTranscoding)}).Sub(1)
	case livekit.IngressInput_URL_INPUT:
		m.requestGauge.With(prometheus.Labels{"type": "url", "transcoding": fmt.Sprintf("%v", *info.EnableTranscoding)}).Sub(1)
	}
}

func (m *Monitor) getAvailable(minIdleRatio float64) float64 {
	return m.cpuStats.GetCPUIdle() - m.pendingCPUs.Load() - minIdleRatio*m.cpuStats.NumCPU()
}
