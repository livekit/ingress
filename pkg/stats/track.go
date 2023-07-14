package stats

import (
	"sync"
	"time"

	"github.com/frostbyte73/core"

	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/types"
)

type MediaStatsReporter struct {
	params *params.Params

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

func NewMediaStats() *MediaStatsReporter {
	m := &MediaStatsReporter{
		stats: make(map[types.StreamKind]*trackStats),
		done:  core.NewFuse(),
	}

	go func() {
		m.runMediaStatsCollector()
	}()

	return m
}

func (m *MediaStatsReporter) AddTrack(kind types.StreamKind) {
	m.lock.Lock()
	defer m.lock.Unlock()

	if _, ok := m.stats[kind]; !ok {
		m.stats[kind] = &trackStats{}
	}
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
			m.updateIngressState()
		case <-m.done.Watch():
			return
		}
	}
}

func (m *MediaStatsReporter) updateIngressState() {
	var audioOk, videoOk bool
	var audioAverageBps, audioCurrentBps, videoAverageBps, videoCurrentBps int
	var s *trackStats

	m.lock.Lock()
	if audioOk, s = m.stats[types.Audio]; audioOk {
		audioAvergaeBps, audioCurrentBps := s.getStats()
	}
	if videoOk, s := m.stats[types.Video]; videoOk {
		videoAvergaeBps, videoCurrentBps := s.getStats()
	}
	m.lock.Unlock()

	if audioOk {
		m.params.SetInputAudioBitrate(audioAverageBps, audioCurrentBps)
	}
	if videoOk {
		m.params.SetInputVideoBitrate(videoAverageBps, videoCurrentBps)
	}

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

func (s *trackStats) getStats() (int, int) {
	now := time.Now()

	averageBps := int(totalBytes / int64(now.Sub(s.startTime)))
	currentBps := int(s.currentBytes / int64(now.Sub(s.lastQueryTime)))

	s.lastQueryTime = now
	s.currentBytes = 0

	return averageBps, currentBps
}
