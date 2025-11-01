// Copyright 2024 LiveKit, Inc.
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
	"context"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/livekit/ingress/pkg/ipc"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/logger"
)

const (
	InputAudio  = "input.audio"
	InputVideo  = "input.video"
	OutputAudio = "output.audio"
	OutputVideo = "output.video"
)

var (
	latencyMetricOnce sync.Once
	latencyHistogram  *prometheus.HistogramVec
)

func getLatencyHistogram() *prometheus.HistogramVec {
	latencyMetricOnce.Do(func() {
		latencyHistogram = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "livekit",
				Subsystem: "ingress",
				Name:      "packet_latency_seconds",
				Help:      "Observed end-to-end packet latency within the ingress pipeline",
				Buckets:   prometheus.ExponentialBuckets(0.005, 2, 12),
			},
			[]string{"track", "ingress_type"},
		)
		prometheus.MustRegister(latencyHistogram)
	})

	return latencyHistogram
}

type MediaStatsReporter struct {
	lock sync.Mutex

	statsUpdater  types.MediaStatsUpdater
	statGatherers []types.MediaStatGatherer
	ingressType   string

	latencyMetric *prometheus.HistogramVec

	done core.Fuse
}

type LocalStatsUpdater struct {
	Params *params.Params
}

type LocalMediaStatsGatherer struct {
	lock  sync.Mutex
	stats []*MediaTrackStatGatherer
}

func NewMediaStats(statsUpdater types.MediaStatsUpdater, ingressType string) *MediaStatsReporter {
	m := &MediaStatsReporter{
		statsUpdater:  statsUpdater,
		latencyMetric: getLatencyHistogram(),
		ingressType:   ingressType,
	}

	go func() {
		m.runMediaStatsCollector()
	}()

	return m
}

func (m *MediaStatsReporter) Close() {
	m.done.Break()
}

func (m *MediaStatsReporter) RegisterGatherer(g types.MediaStatGatherer) {
	m.lock.Lock()
	defer m.lock.Unlock()

	m.statGatherers = append(m.statGatherers, g)
}

func (m *MediaStatsReporter) UpdateStats(ctx context.Context) {
	res := &ipc.MediaStats{
		TrackStats: make(map[string]*ipc.TrackStats),
	}

	m.lock.Lock()
	for _, l := range m.statGatherers {
		ms, err := l.GatherStats(ctx)
		if err != nil {
			logger.Infow("failed gather media stats", "error", err)
			continue
		}

		if ms == nil {
			continue
		}

		// Merge the result. Keys are assumed to be exclusive
		for k, v := range ms.TrackStats {
			if len(v.PacketLatencySeconds) > 0 && m.latencyMetric != nil {
				hist := m.latencyMetric.WithLabelValues(k, m.ingressType)
				for _, sample := range v.PacketLatencySeconds {
					hist.Observe(sample)
				}
				v.PacketLatencySeconds = nil
			}
			res.TrackStats[k] = v
		}

	}
	m.lock.Unlock()

	m.statsUpdater.UpdateMediaStats(ctx, res)
}

func (m *MediaStatsReporter) runMediaStatsCollector() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// TODO extend core.Fuse to provide a context?
			m.UpdateStats(context.Background())
		case <-m.done.Watch():
			return
		}
	}
}

func NewLocalMediaStatsGatherer() *LocalMediaStatsGatherer {
	return &LocalMediaStatsGatherer{}
}

func (l *LocalMediaStatsGatherer) RegisterTrackStats(path string) *MediaTrackStatGatherer {
	g := NewMediaTrackStatGatherer(path)

	l.lock.Lock()
	defer l.lock.Unlock()

	l.stats = append(l.stats, g)

	return g
}

func (l *LocalMediaStatsGatherer) GatherStats(ctx context.Context) (*ipc.MediaStats, error) {
	ms := &ipc.MediaStats{
		TrackStats: make(map[string]*ipc.TrackStats),
	}

	l.lock.Lock()
	for _, ts := range l.stats {
		s := ts.UpdateStats()
		ms.TrackStats[ts.Path()] = s
	}
	l.lock.Unlock()

	return ms, nil
}

func (a *LocalStatsUpdater) UpdateMediaStats(ctx context.Context, s *ipc.MediaStats) error {
	audioStats, ok := s.TrackStats[InputAudio]
	if ok {
		a.Params.SetInputAudioStats(audioStats)
	}

	videoStats, ok := s.TrackStats[InputVideo]
	if ok {
		a.Params.SetInputVideoStats(videoStats)
	}

	LogMediaStats(s, a.Params.GetLogger())

	return nil
}

func LogMediaStats(s *ipc.MediaStats, logger logger.Logger) {
	for k, v := range s.TrackStats {
		logger.Infow("track stats update", "name", k, "currentBitrate", v.CurrentBitrate, "averageBitrate", v.AverageBitrate, "currentPackets", v.CurrentPackets, "totalPacket", v.TotalPackets, "currentLossRate", v.CurrentLossRate, "totalLossRate", v.TotalLossRate, "currentPLI", v.CurrentPli, "totalPLI", v.TotalPli, "jitter", v.Jitter)
	}
}
