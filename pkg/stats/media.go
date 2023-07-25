package stats

import (
	"context"
	"sync"
	"time"

	"github.com/frostbyte73/core"

	"github.com/livekit/ingress/pkg/types"
)

type MediaStatsReporter struct {
	sessionAPI types.SessionAPI

	lock  sync.Mutex
	done  core.Fuse
	stats map[types.StreamKind]*trackStats
}

type trackStats struct {
	totalBytes int64
	startTime  time.Time

	currentBytes  int64
	lastQueryTime time.Time
}

func NewMediaStats(sessionAPI types.SessionAPI) *MediaStatsReporter {
	m := &MediaStatsReporter{
		sessionAPI: sessionAPI,
		stats:      make(map[types.StreamKind]*trackStats),
		done:       core.NewFuse(),
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
	ticker := time.NewTicker(5 * time.Second)
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

	m.sessionAPI.UpdateMediaStats(ctx, ms)

	// TODO send analytics update
}

func (s *trackStats) mediaReceived(size int64) {
	if s.startTime.IsZero() {
		now := time.Now()

		s.startTime = now
		s.lastQueryTime = now
	}

	s.totalBytes += size
	s.currentBytes += size
}

func (s *trackStats) getStats() (uint32, uint32) {
	now := time.Now()

	averageBps := uint32((s.totalBytes * 8) / int64(now.Sub(s.startTime)))
	currentBps := uint32((s.currentBytes * 8) / int64(now.Sub(s.lastQueryTime)))

	s.lastQueryTime = now
	s.currentBytes = 0

	return averageBps, currentBps
}
