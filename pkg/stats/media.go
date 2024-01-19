package stats

import (
	"context"
	"sync"
	"time"

	"github.com/frostbyte73/core"

	"github.com/livekit/ingress/pkg/ipc"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/types"
)

const (
	InputAudio = "input.audio"
	InputVideo = "input.video"
)

type MediaStatsReporter struct {
	statsUpdater types.MediaStatsUpdater

	lock  sync.Mutex
	done  core.Fuse
	stats []*MediaTrackStatGatherer
}

type LocalStatsUpdater struct {
	Params *params.Params
}

func NewMediaStats(statsUpdater types.MediaStatsUpdater) *MediaStatsReporter {
	m := &MediaStatsReporter{
		statsUpdater: statsUpdater,
		done:         core.NewFuse(),
	}

	go func() {
		m.runMediaStatsCollector()
	}()

	return m
}

func (m *MediaStatsReporter) Close() {
	m.done.Break()
}

func (m *MediaStatsReporter) RegisterTrackStats(path string) *MediaTrackStatGatherer {
	g := NewMediaTrackStatGatherer(path)

	m.lock.Lock()
	defer m.lock.Unlock()

	m.stats = append(m.stats, g)

	return g
}

func (m *MediaStatsReporter) runMediaStatsCollector() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// TODO extend core.Fuse to provide a context?
			m.updateStats(context.Background())
		case <-m.done.Watch():
			return
		}
	}
}

func (m *MediaStatsReporter) updateStats(ctx context.Context) {
	ms := &ipc.MediaStats{
		TrackStats: make(map[string]*ipc.TrackStats),
	}

	m.lock.Lock()
	for _, ts := range m.stats {
		s := ts.UpdateStats()
		ms.TrackStats[ts.Path()] = s
	}
	m.lock.Unlock()

	m.statsUpdater.UpdateMediaStats(ctx, ms)
}

func (a *LocalStatsUpdater) UpdateMediaStats(ctx context.Context, s *ipc.MediaStats) error {
	audioStats, ok := s.TrackStats[InputAudio]
	if ok {
		a.Params.SetInputAudioBitrate(audioStats.AverageBitrate, audioStats.CurrentBitrate)
	}

	videoStats, ok := s.TrackStats[InputVideo]
	if ok {
		a.Params.SetInputVideoBitrate(videoStats.AverageBitrate, videoStats.CurrentBitrate)
	}

	return nil
}
