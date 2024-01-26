package stats

import (
	"math"
	"sync"
	"time"

	"github.com/livekit/ingress/pkg/ipc"

	morestats "github.com/aclements/go-moremath/stats"
)

const (
	maxJitterStatsLen = 100_000
)

type MediaTrackStatGatherer struct {
	lock sync.Mutex

	path string

	totalBytes   int64
	totalPackets int64
	totalLost    int64
	startTime    time.Time

	currentBytes   int64
	currentPackets int64
	currentLost    int64
	lastQueryTime  time.Time

	lastPacketTime     time.Time
	lastPacketInterval time.Duration
	jitter             morestats.Sample
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

	g.totalPackets++
	g.currentPackets++

	var packetInterval time.Duration
	if !g.lastPacketTime.IsZero() {
		packetInterval = now.Sub(g.lastPacketTime)
	}
	if g.lastPacketInterval != 0 {
		jitter := packetInterval - g.lastPacketInterval

		if len(g.jitter.Xs) < maxJitterStatsLen {
			g.jitter.Xs = append(g.jitter.Xs, math.Abs(float64(jitter)/float64(time.Millisecond)))
		}
	}

	g.lastPacketInterval = packetInterval
	g.lastPacketTime = now
}

func (g *MediaTrackStatGatherer) PacketLost(count int64) {
	g.lock.Lock()
	defer g.lock.Unlock()

	g.currentLost += count
	g.totalLost += count
}

func (g *MediaTrackStatGatherer) UpdateStats() *ipc.TrackStats {
	g.lock.Lock()
	defer g.lock.Unlock()

	now := time.Now()

	averageBps := uint32(float64(g.totalBytes) * 8 * float64(time.Second) / float64(now.Sub(g.startTime)))
	currentBps := uint32(float64(g.currentBytes) * 8 * float64(time.Second) / float64(now.Sub(g.lastQueryTime)))

	currentLossRate := float64(g.currentLost) / float64(g.currentPackets)

	g.lastQueryTime = now
	g.currentBytes = 0
	g.currentPackets = 0
	g.currentLost = 0

	jitter := g.jitter.Sort() // To make quantile computation faster

	jitterStats := &ipc.JitterStats{
		P50: jitter.Quantile(0.5),
		P90: jitter.Quantile(0.9),
		P99: jitter.Quantile(0.99),
	}

	g.jitter.Xs = nil
	g.jitter.Sorted = false

	return &ipc.TrackStats{
		AverageBitrate:  averageBps,
		CurrentBitrate:  currentBps,
		TotalPackets:    uint64(g.totalPackets),
		CurrentPackets:  uint64(g.currentPackets),
		TotalLossRate:   float64(g.totalLost) / float64(g.totalPackets),
		CurrentLossRate: currentLossRate,
		Jitter:          jitterStats,
	}
}

func (g *MediaTrackStatGatherer) Path() string {
	return g.path
}
