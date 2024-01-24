package stats

import (
	"math"
	"sync"
	"time"

	"github.com/livekit/ingress/pkg/ipc"

	morestats "github.com/aclements/go-moremath/stats"
)

type MediaTrackStatGatherer struct {
	lock sync.Mutex

	path string

	totalBytes int64
	startTime  time.Time

	currentBytes  int64
	lastQueryTime time.Time

	lastPacketTime     time.Time
	lastPacketInterval time.Duration
	jitter             morestats.Sample

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

	now := time.Now()

	if g.startTime.IsZero() {
		g.startTime = now
		g.lastQueryTime = now
	}

	g.totalBytes += size
	g.currentBytes += size

	var packetInterval time.Duration
	if !g.lastPacketTime.IsZero() {
		packetInterval = now.Sub(g.lastPacketTime)
	}
	if g.lastPacketInterval != 0 {
		jitter := packetInterval - g.lastPacketInterval

		g.jitter.Xs = append(g.jitter.Xs, math.Abs(float64(jitter)/float64(time.Millisecond)))
	}

	g.lastPacketInterval = packetInterval
	g.lastPacketTime = now
}

func (g *MediaTrackStatGatherer) UpdateStats() *ipc.TrackStats {
	g.lock.Lock()
	defer g.lock.Unlock()

	now := time.Now()

	averageBps := uint32(float64(g.totalBytes) * 8 * float64(time.Second) / float64(now.Sub(g.startTime)))
	currentBps := uint32(float64(g.currentBytes) * 8 * float64(time.Second) / float64(now.Sub(g.lastQueryTime)))

	g.lastQueryTime = now
	g.currentBytes = 0

	jitter := g.jitter.Sort() // To make quantile computation faster

	jitterStats := &ipc.JitterStats{
		P50: jitter.Quantile(0.5),
		P90: jitter.Quantile(0.9),
		P99: jitter.Quantile(0.99),
	}

	g.jitter.Xs = nil
	g.jitter.Sorted = false

	return &ipc.TrackStats{
		AverageBitrate: averageBps,
		CurrentBitrate: currentBps,
		Jitter:         jitterStats,
	}
}

func (g *MediaTrackStatGatherer) Path() string {
	return g.path
}
