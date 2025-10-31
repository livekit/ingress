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
	"math"
	"sync"
	"time"

	"github.com/livekit/ingress/pkg/ipc"

	morestats "github.com/aclements/go-moremath/stats"
)

const (
	maxJitterStatsLen   = 100_000
	maxLatencySampleLen = 5_000
)

type MediaTrackStatGatherer struct {
	lock sync.Mutex

	path string

	totalBytes   int64
	totalPackets int64
	totalLost    int64
	totalPLI     int64
	startTime    time.Time

	currentBytes   int64
	currentPackets int64
	currentLost    int64
	currentPLI     int64
	lastQueryTime  time.Time

	lastPacketTime     time.Time
	lastPacketInterval time.Duration
	jitter             morestats.Sample
	packetLatency      []float64
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

func (g *MediaTrackStatGatherer) PLI() {
	g.lock.Lock()
	defer g.lock.Unlock()

	g.currentPLI++
	g.totalPLI++
}

func (g *MediaTrackStatGatherer) ObserveLatency(latency time.Duration) {
	g.lock.Lock()
	defer g.lock.Unlock()

	if latency < 0 {
		return
	}

	if len(g.packetLatency) >= maxLatencySampleLen {
		copy(g.packetLatency, g.packetLatency[1:])
		g.packetLatency = g.packetLatency[:maxLatencySampleLen-1]
	}

	g.packetLatency = append(g.packetLatency, latency.Seconds())
}

func (g *MediaTrackStatGatherer) UpdateStats() *ipc.TrackStats {
	g.lock.Lock()
	defer g.lock.Unlock()

	now := time.Now()

	averageBps := uint32(float64(g.totalBytes) * 8 * float64(time.Second) / float64(now.Sub(g.startTime)))
	currentBps := uint32(float64(g.currentBytes) * 8 * float64(time.Second) / float64(now.Sub(g.lastQueryTime)))

	currentLossRate := float64(g.currentLost) / float64(g.currentPackets)

	jitter := g.jitter.Sort() // To make quantile computation faster

	jitterStats := &ipc.JitterStats{
		P50: jitter.Quantile(0.5),
		P90: jitter.Quantile(0.9),
		P99: jitter.Quantile(0.99),
	}

	g.jitter.Xs = nil
	g.jitter.Sorted = false

	st := &ipc.TrackStats{
		AverageBitrate:  averageBps,
		CurrentBitrate:  currentBps,
		TotalPackets:    uint64(g.totalPackets),
		CurrentPackets:  uint64(g.currentPackets),
		TotalLossRate:   float64(g.totalLost) / float64(g.totalPackets),
		CurrentLossRate: currentLossRate,
		TotalPli:        uint64(g.totalPLI),
		CurrentPli:      uint64(g.currentPLI),
		Jitter:          jitterStats,
	}

	if len(g.packetLatency) > 0 {
		st.PacketLatencySeconds = append(st.PacketLatencySeconds, g.packetLatency...)
	}

	g.lastQueryTime = now
	g.currentBytes = 0
	g.currentPackets = 0
	g.currentLost = 0
	g.currentPLI = 0
	g.packetLatency = nil

	return st
}

func (g *MediaTrackStatGatherer) Path() string {
	return g.path
}
