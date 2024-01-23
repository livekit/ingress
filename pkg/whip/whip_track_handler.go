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
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/server-sdk-go/pkg/jitter"
	"github.com/livekit/server-sdk-go/pkg/synchronizer"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
)

const (
	maxVideoLatency = 600 * time.Millisecond
	maxAudioLatency = time.Second
)

type MediaSink interface {
	PushRTP(pkt *rtp.Packet) error
	Close() error
}

type whipTrackHandler struct {
	logger      logger.Logger
	remoteTrack *webrtc.TrackRemote
	receiver    *webrtc.RTPReceiver
	mediaSink   MediaSink
	writePLI    func(ssrc webrtc.SSRC)
	onRTCP      func(packet rtcp.Packet)

	jb             *jitter.Buffer
	doJitterBuffer bool

	statsLock  sync.Mutex
	trackStats *stats.MediaTrackStatGatherer

	firstPacket sync.Once
	fuse        core.Fuse
}

func newWHIPTrackHandler(
	logger logger.Logger,
	track *webrtc.TrackRemote,
	receiver *webrtc.RTPReceiver,
	sync *synchronizer.TrackSynchronizer,
	mediaSink MediaSink,
	writePLI func(ssrc webrtc.SSRC),
	onRTCP func(packet rtcp.Packet),
	doJitterBuffer bool,
) (*whipTrackHandler, error) {
	var err error
	t := &whipTrackHandler{
		logger:         logger,
		remoteTrack:    track,
		receiver:       receiver,
		mediaSink:      mediaSink,
		writePLI:       writePLI,
		onRTCP:         onRTCP,
		fuse:           core.NewFuse(),
		doJitterBuffer: doJitterBuffer,
	}

	t.jb, err = t.createJitterBuffer()
	if err != nil {
		return nil, err
	}

	return t, nil
}

func (t *whipTrackHandler) Start(onDone func(err error)) (err error) {
	t.startRTPReceiver(onDone)
	if t.onRTCP != nil {
		t.startRTCPReceiver()
	}

	return nil
}

func (t *whipTrackHandler) SetMediaTrackStatsGatherer(stats *stats.MediaTrackStatGatherer) {
	t.statsLock.Lock()
	defer t.statsLock.Unlock()

	t.trackStats = stats
}

func (t *whipTrackHandler) Close() {
	t.fuse.Break()
}

func (t *whipTrackHandler) startRTPReceiver(onDone func(err error)) {
	go func() {
		var err error
		defer func() {
			t.mediaSink.Close()
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
				err = nil
				return
			default:
				err = t.processRTPPacket()
				switch err {
				case nil:
				case io.EOF:
					err = nil
					return
				default:
					if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
						continue
					}

					t.logger.Warnw("error reading rtp packets", err)
					return
				}
			}
		}
	}()
}

func (t *whipTrackHandler) processRTPPacket() error {
	_ = t.remoteTrack.SetReadDeadline(time.Now().Add(time.Millisecond * 500))

	pkt, _, err := t.remoteTrack.ReadRTP()
	if err != nil {
		return err
	}

	t.firstPacket.Do(func() {
		t.logger.Debugw("first packet received")
	})

	t.statsLock.Lock()
	stats := t.trackStats
	t.statsLock.Unlock()
	if stats != nil {
		stats.MediaReceived(int64(len(pkt.Payload)))
	}

	if !t.doJitterBuffer {
		return t.mediaSink.PushRTP(pkt)

	}

	t.jb.Push(pkt)
	for _, pkt := range t.jb.Pop(false) {
		if err := t.mediaSink.PushRTP(pkt); err != nil {
			return err
		}
	}

	return nil
}

func (t *whipTrackHandler) startRTCPReceiver() {
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

func (t *whipTrackHandler) createJitterBuffer() (*jitter.Buffer, error) {
	var maxLatency time.Duration
	var depacketizer rtp.Depacketizer
	options := []jitter.Option{jitter.WithLogger(t.logger)}

	switch strings.ToLower(t.remoteTrack.Codec().MimeType) {
	case strings.ToLower(webrtc.MimeTypeVP8):
		maxLatency = maxVideoLatency
		options = append(options, jitter.WithPacketDroppedHandler(func() { t.writePLI(t.remoteTrack.SSRC()) }))
		depacketizer = &codecs.VP8Packet{}

	case strings.ToLower(webrtc.MimeTypeH264):
		maxLatency = maxVideoLatency
		options = append(options, jitter.WithPacketDroppedHandler(func() { t.writePLI(t.remoteTrack.SSRC()) }))
		depacketizer = &codecs.H264Packet{}

	case strings.ToLower(webrtc.MimeTypeOpus):
		maxLatency = maxAudioLatency
		depacketizer = &codecs.OpusPacket{}
		// No PLI for audio

	default:
		return nil, errors.ErrUnsupportedDecodeMimeType(t.remoteTrack.Codec().MimeType)
	}

	clockRate := t.remoteTrack.Codec().ClockRate

	jb := jitter.NewBuffer(depacketizer, clockRate, maxLatency, options...)

	return jb, nil
}
