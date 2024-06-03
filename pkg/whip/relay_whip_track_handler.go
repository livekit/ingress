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

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/server-sdk-go/v2/pkg/jitter"
	"github.com/livekit/server-sdk-go/v2/pkg/synchronizer"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

const (
	maxVideoLatency = 600 * time.Millisecond
	maxAudioLatency = time.Second
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
	lastSn      uint16
	lastSnValid bool

	statsLock  sync.Mutex
	trackStats *stats.MediaTrackStatGatherer
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
	jb, err := createJitterBuffer(track, logger, writePLI)
	if err != nil {
		return nil, err
	}
	depacketizer, err := createDepacketizer(track)
	if err != nil {
		return nil, err
	}
	relaySink := NewRelayMediaSink(logger)

	return &RelayWhipTrackHandler{
		logger:       logger,
		remoteTrack:  track,
		quality:      quality,
		receiver:     receiver,
		relaySink:    relaySink,
		sync:         sync,
		jb:           jb,
		onRTCP:       onRTCP,
		depacketizer: depacketizer,
	}, nil
}

func (t *RelayWhipTrackHandler) Start(onDone func(err error)) (err error) {
	t.startRTPReceiver(onDone)
	if t.onRTCP != nil {
		t.startRTCPReceiver()
	}

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
				t.logger.Debugw("stopping rtp receiver")
				return
			default:
				err = t.processRTPPacket()
				switch err {
				case nil, errors.ErrPrerollBufferReset:
					// continue
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

	return t.pushRTP(pkt)
}

func (t *RelayWhipTrackHandler) pushRTP(pkt *rtp.Packet) error {
	t.firstPacket.Do(func() {
		t.logger.Debugw("first packet received")
		t.sync.Initialize(pkt)
	})

	t.jb.Push(pkt)

	samples := t.jb.PopSamples(false)
	for _, pkts := range samples {
		if len(pkts) == 0 {
			continue
		}

		var ts time.Duration
		var err error
		var buffer bytes.Buffer // TODO reuse the same buffer across calls, after resetting it if buffer allocation is a performane bottleneck
		for _, pkt := range pkts {
			ts, err = t.sync.GetPTS(pkt)
			switch err {
			case nil, synchronizer.ErrBackwardsPTS:
				err = nil
			default:
				return err
			}

			t.statsLock.Lock()
			stats := t.trackStats
			t.statsLock.Unlock()

			if t.lastSnValid && t.lastSn+1 != pkt.SequenceNumber {
				gap := pkt.SequenceNumber - t.lastSn
				if t.lastSn-pkt.SequenceNumber < gap {
					gap = t.lastSn - pkt.SequenceNumber
				}
				if stats != nil {
					stats.PacketLost(int64(gap - 1))
				}
			}

			t.lastSnValid = true
			t.lastSn = pkt.SequenceNumber

			if len(pkt.Payload) <= 2 {
				// Padding
				continue
			}

			buf, err := t.depacketizer.Unmarshal(pkt.Payload)
			if err != nil {
				logger.Warnw("failed unmarshalling RTP payload", err, "pkt", pkt, "payload", pkt.Payload[:min(len(pkt.Payload), 20)])
				return err
			}

			if stats != nil {
				stats.MediaReceived(int64(len(buf)))
			}

			_, err = buffer.Write(buf)
			if err != nil {
				return err
			}
		}

		// This returns the average duration, not the actual duration of the specific sample
		// SampleBuilder is using the duration of the previous sample, which is inaccurate as well
		sampleDuration := t.sync.GetFrameDuration()

		s := &media.Sample{
			Data:     buffer.Bytes(),
			Duration: sampleDuration,
		}

		err = t.relaySink.PushSample(s, ts)
		if err != nil {
			return err
		}
	}

	return nil
}
