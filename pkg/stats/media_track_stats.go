package stats

import (
	"time"

	"github.com/livekit/ingress/pkg/ipc"
)

type MediaTrackStatGatherer struct {
	path string

	totalBytes int64
	startTime  time.Time

	currentBytes  int64
	lastQueryTime time.Time

	stats *ipc.TrackStats
}

func NewMediaTrackStatGatherer(path string) *MediaTrackStatGatherer {
	return &MediaTrackStatGatherer{
		path: path,
	}
}

func (g *MediaTrackStatGatherer) MediaReceived(size int64) {
	if s.startTime.IsZero() {
		now := time.Now()

		s.startTime = now
		s.lastQueryTime = now
	}

	s.totalBytes += size
	s.currentBytes += size
}

func (g *MediaTrackStatGatherer) UpdateStats() *ipc.TrackStats {
	now := time.Now()

	averageBps := uint32(float64(s.totalBytes) * 8 * float64(time.Second) / float64(now.Sub(s.startTime)))
	currentBps := uint32(float64(s.currentBytes) * 8 * float64(time.Second) / float64(now.Sub(s.lastQueryTime)))

	s.lastQueryTime = now
	s.currentBytes = 0

	return &ipc.TrackStats{
		AverageBitrate: averageBps,
		CurrentBitrate: currentBps,
	}
}

func (g *MediaTrackStatGatherer) Path() string {
	return g.path
}
