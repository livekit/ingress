package stats

import (
	"context"
	"sync"
	"time"

	"github.com/frostbyte73/core"

	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/types"
)

type MediaStatsReporter struct {
	statsUpdater types.MediaStatsUpdater

	lock  sync.Mutex
	done  core.Fuse
	stats map[types.StreamKind]*trackStats
}

type LocalStatsUpdater struct {
	Params *params.Params
}

func NewMediaStats(statsUpdater types.MediaStatsUpdater) *MediaStatsReporter {
	m := &MediaStatsReporter{
		statsUpdater: statsUpdater,
		stats:        make(map[types.StreamKind]*trackStats),
		done:         core.NewFuse(),
	}

	go func() {
		m.runMediaStatsCollector()
	}()

	return m
}

func (m *MediaStatsReporter) MediaReceived(kind types.StreamKind, size int64) {
	m.lock.Lock()
	defer m.lock.Unlock()

	ts, ok := m.stats[kind]
	if !ok {
		ts = &trackStats{}
		m.stats[kind] = ts
	}

	ts.mediaReceived(size)
}

func (m *MediaStatsReporter) Close() {
	m.done.Break()
}

func (m *MediaStatsReporter) runMediaStatsCollector() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// TODO extend core.Fuse to provide a context?
			m.updateIngressState(context.Background())
		case <-m.done.Watch():
			return
		}
	}
}

func (m *MediaStatsReporter) updateIngressState(ctx context.Context) {
	var audioOk, videoOk bool
	var audioAverageBps, audioCurrentBps, videoAverageBps, videoCurrentBps uint32
	var s *trackStats

	m.lock.Lock()
	if s, audioOk = m.stats[types.Audio]; audioOk {
		audioAverageBps, audioCurrentBps = s.getStats()
	}
	if s, videoOk = m.stats[types.Video]; videoOk {
		videoAverageBps, videoCurrentBps = s.getStats()
	}
	m.lock.Unlock()

	ms := &types.MediaStats{}

	if audioOk {
		ms.AudioAverageBitrate = &audioAverageBps
		ms.AudioCurrentBitrate = &audioCurrentBps
	}
	if videoOk {
		ms.VideoAverageBitrate = &videoAverageBps
		ms.VideoCurrentBitrate = &videoCurrentBps
	}

	m.statsUpdater.UpdateMediaStats(ctx, ms)
}

func (a *LocalStatsUpdater) UpdateMediaStats(ctx context.Context, s *types.MediaStats) error {
	if s.AudioAverageBitrate != nil && s.AudioCurrentBitrate != nil {
		a.Params.SetInputAudioBitrate(*s.AudioAverageBitrate, *s.AudioCurrentBitrate)
	}

	if s.VideoAverageBitrate != nil && s.VideoCurrentBitrate != nil {
		a.Params.SetInputVideoBitrate(*s.VideoAverageBitrate, *s.VideoCurrentBitrate)
	}

	return nil
}
