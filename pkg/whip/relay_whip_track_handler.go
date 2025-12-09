// Copyright 2023 LiveKit, Inc.
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

package whip

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"go.uber.org/atomic"

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/media-sdk/jitter"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/server-sdk-go/v2/pkg/synchronizer"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

const (
	maxVideoLatency     = 600 * time.Millisecond
	maxAudioLatency     = time.Second
	statsReportInterval = 3 * time.Second
)

type RelayWhipTrackHandler struct {
	logger       logger.Logger
	remoteTrack  *webrtc.TrackRemote
	depacketizer rtp.Depacketizer
	quality      livekit.VideoQuality
	receiver     *webrtc.RTPReceiver
	sync         *synchronizer.TrackSynchronizer
	writePLI     func(ssrc webrtc.SSRC)
	onRTCP       func(packet rtcp.Packet)

	jb        *jitter.Buffer
	relaySink *RelayMediaSink

	firstPacket sync.Once
	fuse        core.Fuse

	lastError atomic.Error

	statsLock  sync.Mutex
	trackStats *stats.MediaTrackStatGatherer

	statsTicker *time.Ticker
	lastJBStats jitter.BufferStats
}

func NewRelayWhipTrackHandler(
	logger logger.Logger,
	track *webrtc.TrackRemote,
	quality livekit.VideoQuality,
	sync *synchronizer.TrackSynchronizer,
	receiver *webrtc.RTPReceiver,
	writePLI func(ssrc webrtc.SSRC),
	onRTCP func(packet rtcp.Packet),
) (*RelayWhipTrackHandler, error) {
	t := &RelayWhipTrackHandler{
		logger:      logger,
		remoteTrack: track,
		quality:     quality,
		receiver:    receiver,
		sync:        sync,
		writePLI:    writePLI,
		onRTCP:      onRTCP,
	}
	jb, err := createJitterBuffer(track, logger, writePLI, t.onPacket)
	if err != nil {
		return nil, err
	}
	depacketizer, err := createDepacketizer(track)
	if err != nil {
		return nil, err
	}
	relaySink := NewRelayMediaSink(logger)

	t.jb = jb
	t.depacketizer = depacketizer
	t.relaySink = relaySink

	return t, nil
}

func (t *RelayWhipTrackHandler) Start(onDone func(err error)) (err error) {
	t.startRTPReceiver(onDone)
	if t.onRTCP != nil {
		t.startRTCPReceiver()
	}
	t.startStatsReporter()

	return nil
}

func (t *RelayWhipTrackHandler) SetMediaTrackStatsGatherer(st *stats.LocalMediaStatsGatherer) {
	t.statsLock.Lock()

	var path string

	switch t.remoteTrack.Kind() {
	case webrtc.RTPCodecTypeAudio:
		path = stats.InputAudio
	case webrtc.RTPCodecTypeVideo:
		path = fmt.Sprintf("%s.%s", stats.InputVideo, t.quality)
	default:
		path = "input.unknown"
	}

	g := st.RegisterTrackStats(path)
	t.trackStats = g
	if jbStats := t.jb.Stats(); jbStats != nil {
		t.lastJBStats = *jbStats
	}

	t.statsLock.Unlock()
}

func (t *RelayWhipTrackHandler) SetWriter(w io.WriteCloser) error {
	return t.relaySink.SetWriter(w)
}

func (t *RelayWhipTrackHandler) Close() {
	t.fuse.Break()
}

func (t *RelayWhipTrackHandler) startRTPReceiver(onDone func(err error)) {
	go func() {
		var err error
		defer func() {
			t.relaySink.Close()
			if onDone != nil {
				onDone(err)
			}
		}()

		t.logger.Infow("starting rtp receiver")

		if t.remoteTrack.Kind() == webrtc.RTPCodecTypeVideo && t.writePLI != nil {
			t.writePLI(t.remoteTrack.SSRC())
		}

		for {
			select {
			case <-t.fuse.Watch():
				if t.lastError.Load() != nil {
					err = t.lastError.Load()
					t.logger.Warnw("error forwarding rtp packets", err)
				}
				t.logger.Debugw("stopping rtp receiver")
				return
			default:
				err = t.processRTPPacket()
				switch err {
				case nil:
					continue
				case io.EOF:
					err = nil // success
					return
				default:
					if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
						continue
					}

					t.logger.Warnw("error forwarding rtp packets", err)
					return
				}
			}
		}
	}()
}

func (t *RelayWhipTrackHandler) startRTCPReceiver() {
	go func() {
		t.logger.Infow("starting app source rtcp receiver")

		for {
			select {
			case <-t.fuse.Watch():
				t.logger.Debugw("stopping app source rtcp receiver")
				return
			default:
				_ = t.receiver.SetReadDeadline(time.Now().Add(time.Millisecond * 500))
				pkts, _, err := t.receiver.ReadRTCP()

				switch {
				case err == nil:
					// continue
				case err == io.EOF:
					return
				default:
					if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
						continue
					}

					t.logger.Warnw("error reading rtcp", err)
					return
				}

				for _, pkt := range pkts {
					t.onRTCP(pkt)
				}
			}
		}
	}()
}
func (t *RelayWhipTrackHandler) processRTPPacket() error {
	var pkt *rtp.Packet

	_ = t.remoteTrack.SetReadDeadline(time.Now().Add(time.Millisecond * 500))

	pkt, _, err := t.remoteTrack.ReadRTP()
	if err != nil {
		return err
	}

	t.pushRTP(pkt)
	return nil
}

func (t *RelayWhipTrackHandler) pushRTP(pkt *rtp.Packet) {
	t.firstPacket.Do(func() {
		t.logger.Debugw("first packet received")
		t.sync.Initialize(pkt)
	})

	t.jb.Push(pkt)
}

func (t *RelayWhipTrackHandler) onPacket(sample []jitter.ExtPacket) {
	var buffer bytes.Buffer
	var ts time.Duration
	var err error

	for _, pkt := range sample {
		ts, err = t.sync.GetPTS(pkt)
		if err != nil {
			if errors.Is(err, synchronizer.ErrPacketTooOld) {
				continue
			}
			t.lastError.Store(err)
			t.fuse.Break()
			return
		}

		t.statsLock.Lock()
		stats := t.trackStats
		t.statsLock.Unlock()

		if len(pkt.Payload) <= 2 {
			// Padding
			continue
		}

		buf, err := t.depacketizer.Unmarshal(pkt.Payload)
		if err != nil {
			t.logger.Warnw("failed unmarshalling RTP payload", err, "pkt", pkt, "payload", pkt.Payload[:min(len(pkt.Payload), 20)])
			t.lastError.Store(err)
			t.fuse.Break()
			return
		}

		if stats != nil {
			stats.MediaReceived(int64(len(buf)))
		}

		_, err = buffer.Write(buf)
		if err != nil {
			t.lastError.Store(err)
			t.fuse.Break()
			return
		}
	}

	if buffer.Len() == 0 {
		return
	}

	s := &media.Sample{
		Data: buffer.Bytes(),
	}

	err = t.relaySink.PushSample(s, ts)
	if err != nil && !errors.Is(err, errors.ErrPrerollBufferReset) {
		t.lastError.Store(err)
		t.fuse.Break()
		return
	}
}

func (t *RelayWhipTrackHandler) startStatsReporter() {
	if t.statsTicker != nil {
		return
	}

	ticker := time.NewTicker(statsReportInterval)
	t.statsTicker = ticker
	fuseCh := t.fuse.Watch()

	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				t.reportJitterStats()
			case <-fuseCh:
				return
			}
		}
	}()
}

func (t *RelayWhipTrackHandler) reportJitterStats() {
	t.statsLock.Lock()
	g := t.trackStats
	t.statsLock.Unlock()

	jbStats := t.jb.Stats()
	if jbStats == nil {
		return
	}

	lostDelta := int64(jbStats.PacketsLost - t.lastJBStats.PacketsLost)
	droppedDelta := int64(jbStats.PacketsDropped - t.lastJBStats.PacketsDropped)

	totalLoss := lostDelta + droppedDelta
	if totalLoss > 0 && g != nil {
		g.PacketLost(totalLoss)
	}

	t.lastJBStats = *jbStats
}
