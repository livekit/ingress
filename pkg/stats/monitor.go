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
	"os"
	"sort"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/atomic"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils/hwstats"
	"github.com/livekit/psrpc"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/params"
)

const (
	defaultMinIdle float64 = 0.3 // Target at least 30% idle CPU

	promNamespace        = "livekit"
	promSubsystemIngress = "ingress"
	promLabelNodeID      = "node_id"
	promLabelType        = "type"
	promLabelTranscoding = "transcoding"

	publicationStatusSuccess  = "success"
	publicationStatus4xx      = "4xx"
	publicationStatus5xx      = "5xx"
	publicationStatusInternal = "internal"
)

type Monitor struct {
	costConfigLock sync.Mutex
	cpuCostConfig  config.CPUCostConfig
	maxCost        float64

	promCPULoad            prometheus.Gauge
	requestGauge           *prometheus.GaugeVec
	promNodeAvailable      prometheus.GaugeFunc
	promPublicationCounter *prometheus.CounterVec

	// cpuStats (cgroup idle) drives admission; procStats only attributes usage to handler processes.
	// TODO: use one sampler once admission is validated against per-process idle, which misses sub-second processes.
	cpuStats  *hwstats.CPUStats
	procStats *hwstats.CPUStats

	pendingCPUs atomic.Float64

	usageLock sync.Mutex
	usage     map[string]*SessionUsage // resourceID -> usage
	handlers  map[int]*handlerRun      // live handler pid -> run

	started  core.Fuse
	shutdown core.Fuse
}

// SessionUsage is the resource usage of all handler processes run for one session.
type SessionUsage struct {
	IngressID   string
	ResourceID  string
	InputType   livekit.IngressInput
	Transcoding bool

	// Mime types of the input tracks the handler reported, empty until the
	// source has been parsed. A session that ends before then leaves both
	// empty, which is distinct from an audio-only source.
	AudioMimeType string
	VideoMimeType string

	CPUTime   time.Duration // user + system time, from the exited handlers' rusage
	Duration  time.Duration // wall time the handlers ran
	MaxCPU    float64       // highest 1s sample, in cores
	MaxMemory int           // highest sampled RSS, in bytes
}

// AvgCPU is the average number of cores used while a handler ran.
func (u *SessionUsage) AvgCPU() float64 {
	if u.Duration <= 0 {
		return 0
	}
	return u.CPUTime.Seconds() / u.Duration.Seconds()
}

type handlerRun struct {
	usage     *SessionUsage
	startedAt time.Time
}

func NewMonitor() *Monitor {
	return &Monitor{
		usage:    make(map[string]*SessionUsage),
		handlers: make(map[int]*handlerRun),
	}
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
		Namespace:   promNamespace,
		Subsystem:   "node",
		Name:        "cpu_load",
		ConstLabels: prometheus.Labels{promLabelNodeID: conf.NodeID, "node_type": "INGRESS"},
	})
	m.promNodeAvailable = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace:   promNamespace,
		Subsystem:   promSubsystemIngress,
		Name:        "available",
		ConstLabels: prometheus.Labels{promLabelNodeID: conf.NodeID},
	}, func() float64 {
		c := m.CanAccept()
		if c {
			return 1
		}
		return 0
	})
	m.requestGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace:   promNamespace,
		Subsystem:   promSubsystemIngress,
		Name:        "requests",
		ConstLabels: prometheus.Labels{promLabelNodeID: conf.NodeID},
	}, []string{promLabelType, promLabelTranscoding})
	m.promPublicationCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace:   promNamespace,
		Subsystem:   promSubsystemIngress,
		Name:        "publish_status",
		ConstLabels: prometheus.Labels{promLabelNodeID: conf.NodeID},
	}, []string{promLabelType, "status"})

	prometheus.MustRegister(m.promCPULoad, m.promNodeAvailable, m.requestGauge, m.promPublicationCounter)

	// handler usage is diagnostic only, so a failure here does not stop admission
	procStats, err := hwstats.NewProcMonitor(m.updateHandlerStats)
	if err != nil {
		logger.Warnw("failed to start handler process monitor", err)
	} else {
		m.procStats = procStats
	}

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

// Shutdown - server is shutting down, but may stay up for some time for draining
func (m *Monitor) Shutdown() {
	m.shutdown.Break()
}

// Stop the monitor before server termination
func (m *Monitor) Stop() {
	if m.cpuStats != nil {
		m.cpuStats.Stop()
	}
	if m.procStats != nil {
		m.procStats.Stop()
	}

	prometheus.Unregister(m.promCPULoad)
	prometheus.Unregister(m.requestGauge)
	prometheus.Unregister(m.promNodeAvailable)
	prometheus.Unregister(m.promPublicationCounter)
}

// RecordPublicationResult records the outcome of a stream publication attempt,
// bucketing it as success/4xx/5xx/internal. It is a no-op if the monitor has
// not been started (e.g. in standalone test binaries).
func (m *Monitor) RecordPublicationResult(ingressType string, err error) {
	if m == nil || m.promPublicationCounter == nil {
		return
	}

	m.promPublicationCounter.With(prometheus.Labels{
		promLabelType: ingressType,
		"status":      publicationStatus(err),
	}).Inc()
}

func publicationStatus(err error) string {
	if err == nil {
		return publicationStatusSuccess
	}

	// Only an actual 5xx HTTP status from the upstream response is counted as a
	// 5xx error. psrpc errors are classified as either 4xx or internal.
	var httpErr *errors.HTTPError
	if errors.As(err, &httpErr) {
		switch code := httpErr.StatusCode; {
		case code >= 400 && code < 500:
			return publicationStatus4xx
		case code >= 500:
			return publicationStatus5xx
		default:
			return publicationStatusInternal
		}
	}

	var psrpcErr psrpc.Error
	if !errors.As(err, &psrpcErr) {
		// Unstructured error, treat as an internal failure
		return publicationStatusInternal
	}

	switch psrpcErr.Code() {
	case psrpc.Internal, psrpc.Unknown, psrpc.DataLoss:
		return publicationStatusInternal
	}

	if code := psrpcErr.ToHttp(); code >= 400 && code < 500 {
		return publicationStatus4xx
	}

	return publicationStatusInternal
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
		m.requestGauge.With(prometheus.Labels{promLabelType: "rtmp", promLabelTranscoding: fmt.Sprintf("%v", *info.EnableTranscoding)}).Add(1)
	case livekit.IngressInput_WHIP_INPUT:
		m.requestGauge.With(prometheus.Labels{promLabelType: "whip", promLabelTranscoding: fmt.Sprintf("%v", *info.EnableTranscoding)}).Add(1)
	case livekit.IngressInput_URL_INPUT:
		m.requestGauge.With(prometheus.Labels{promLabelType: "url", promLabelTranscoding: fmt.Sprintf("%v", *info.EnableTranscoding)}).Add(1)

	}
}

func (m *Monitor) IngressEnded(info *livekit.IngressInfo) {
	switch info.InputType {
	case livekit.IngressInput_RTMP_INPUT:
		m.requestGauge.With(prometheus.Labels{promLabelType: "rtmp", promLabelTranscoding: fmt.Sprintf("%v", *info.EnableTranscoding)}).Sub(1)
	case livekit.IngressInput_WHIP_INPUT:
		m.requestGauge.With(prometheus.Labels{promLabelType: "whip", promLabelTranscoding: fmt.Sprintf("%v", *info.EnableTranscoding)}).Sub(1)
	case livekit.IngressInput_URL_INPUT:
		m.requestGauge.With(prometheus.Labels{promLabelType: "url", promLabelTranscoding: fmt.Sprintf("%v", *info.EnableTranscoding)}).Sub(1)
	}
}

// HandlerStarted attributes the handler process pid to the session in info.
func (m *Monitor) HandlerStarted(info *livekit.IngressInfo, pid int) {
	resourceID := info.GetState().GetResourceId()

	m.usageLock.Lock()
	defer m.usageLock.Unlock()

	u := m.usage[resourceID]
	if u == nil {
		u = &SessionUsage{
			IngressID:   info.IngressId,
			ResourceID:  resourceID,
			InputType:   info.InputType,
			Transcoding: info.GetEnableTranscoding(),
		}
		m.usage[resourceID] = u
	}
	m.handlers[pid] = &handlerRun{usage: u, startedAt: time.Now()}
}

// HandlerStateUpdated records the input track mime types from a state update
// the handler sent for the session in info. An update for a session without a
// usage record is ignored: the record exists from HandlerStarted until
// SessionEnded, and a late update must not recreate it.
func (m *Monitor) HandlerStateUpdated(info *livekit.IngressInfo) {
	state := info.GetState()
	if state == nil {
		return
	}

	m.usageLock.Lock()
	defer m.usageLock.Unlock()

	u := m.usage[state.GetResourceId()]
	if u == nil {
		return
	}
	if mime := state.GetAudio().GetMimeType(); mime != "" {
		u.AudioMimeType = mime
	}
	if mime := state.GetVideo().GetMimeType(); mime != "" {
		u.VideoMimeType = mime
	}
}

// HandlerExited adds the run of the exited handler pid to its session's usage.
func (m *Monitor) HandlerExited(pid int, state *os.ProcessState) {
	m.usageLock.Lock()
	defer m.usageLock.Unlock()

	h := m.handlers[pid]
	if h == nil {
		return
	}
	delete(m.handlers, pid)

	h.usage.Duration += time.Since(h.startedAt)
	if state != nil {
		h.usage.CPUTime += state.UserTime() + state.SystemTime()
	}
}

// SessionEnded returns the session's handler usage, or nil if no handler ran for it.
func (m *Monitor) SessionEnded(resourceID string) *SessionUsage {
	m.usageLock.Lock()
	defer m.usageLock.Unlock()

	u := m.usage[resourceID]
	if u == nil {
		return nil
	}
	delete(m.usage, resourceID)

	ret := *u
	return &ret
}

func (m *Monitor) updateHandlerStats(stats *hwstats.ProcStats) {
	m.usageLock.Lock()
	defer m.usageLock.Unlock()

	for pid, h := range m.handlers {
		if cpu := stats.Cpu[pid]; cpu > h.usage.MaxCPU {
			h.usage.MaxCPU = cpu
		}
		if gm := stats.Memory[pid]; gm != nil && gm.Total > h.usage.MaxMemory {
			h.usage.MaxMemory = gm.Total
		}
	}
}

func (m *Monitor) getAvailable(minIdleRatio float64) float64 {
	return m.cpuStats.GetCPUIdle() - m.pendingCPUs.Load() - minIdleRatio*m.cpuStats.NumCPU()
}
