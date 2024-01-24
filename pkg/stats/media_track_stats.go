package stats

import (
	"sync"
	"time"

	"github.com/livekit/ingress/pkg/ipc"
)

type MediaTrackStatGatherer struct {
	lock sync.Mutex

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
	g.lock.Lock()
	defer g.lock.Unlock()

	if g.startTime.IsZero() {
		now := time.Now()

		g.startTime = now
		g.lastQueryTime = now
	}

	g.totalBytes += size
	g.currentBytes += size
}

func (g *MediaTrackStatGatherer) UpdateStats() *ipc.TrackStats {
	g.lock.Lock()
	defer g.lock.Unlock()

	now := time.Now()

	averageBps := uint32(float64(g.totalBytes) * 8 * float64(time.Second) / float64(now.Sub(g.startTime)))
	currentBps := uint32(float64(g.currentBytes) * 8 * float64(time.Second) / float64(now.Sub(g.lastQueryTime)))

	g.lastQueryTime = now
	g.currentBytes = 0

	return &ipc.TrackStats{
		AverageBitrate: averageBps,
		CurrentBitrate: currentBps,
	}
}

func (g *MediaTrackStatGatherer) Path() string {
	return g.path
}
