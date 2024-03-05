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
	"fmt"
	"io"
	"net"
	"strings"
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
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

const (
	maxVideoLatency = 600 * time.Millisecond
	maxAudioLatency = time.Second
)

type MediaSink interface {
	PushSample(s *media.Sample, ts time.Duration) error
	Close() error
	SetStatsGatherer(g *stats.LocalMediaStatsGatherer)
}

type whipTrackHandler struct {
	logger       logger.Logger
	quality      livekit.VideoQuality
	remoteTrack  *webrtc.TrackRemote
	receiver     *webrtc.RTPReceiver
	depacketizer rtp.Depacketizer
	jb           *jitter.Buffer
	mediaSink    MediaSink
	sync         *synchronizer.TrackSynchronizer
	writePLI     func(ssrc webrtc.SSRC)
	onRTCP       func(packet rtcp.Packet)

	statsLock   sync.Mutex
	trackStats  *stats.MediaTrackStatGatherer
	lastSn      uint16
	lastSnValid bool

	fuse        core.Fuse
	samplesChan chan sample
}

func newWHIPTrackHandler(
	logger logger.Logger,
	quality livekit.VideoQuality,
	track *webrtc.TrackRemote,
	receiver *webrtc.RTPReceiver,
	sync *synchronizer.TrackSynchronizer,
	mediaSink MediaSink,
	writePLI func(ssrc webrtc.SSRC),
	onRTCP func(packet rtcp.Packet),
) (*whipTrackHandler, error) {
	t := &whipTrackHandler{
		logger:      logger,
		quality:     quality,
		remoteTrack: track,
		receiver:    receiver,
		sync:        sync,
		mediaSink:   mediaSink,
		writePLI:    writePLI,
		onRTCP:      onRTCP,
		fuse:        core.NewFuse(),
		samplesChan: make(chan sample, 10),
	}

	jb, err := t.createJitterBuffer()
	if err != nil {
		return nil, err
	}
	t.jb = jb

	depacketizer, err := t.createDepacketizer()
	if err != nil {
		return nil, err
	}
	t.depacketizer = depacketizer

	return t, nil
}

func (t *whipTrackHandler) Start(onDone func(err error)) (err error) {
	t.startRTPReceiver(onDone)
	if t.onRTCP != nil {
		t.startRTCPReceiver()
	}

	return nil
}

func (t *whipTrackHandler) SetMediaTrackStatsGatherer(st *stats.LocalMediaStatsGatherer) {
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

	t.mediaSink.SetStatsGatherer(st)
}

func (t *whipTrackHandler) Close() {
	t.fuse.Break()
}

func (t *whipTrackHandler) startRTPReceiver(onDone func(err error)) {
	go func() {
		var err error

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
				case nil:
				case io.EOF:
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

	go func() {
		t.mediaWriterWorker(onDone)
	}()

}

// TODO drain on close?
func (t *whipTrackHandler) processRTPPacket() error {
	var pkt *rtp.Packet

	_ = t.remoteTrack.SetReadDeadline(time.Now().Add(time.Millisecond * 500))

	pkt, _, err := t.remoteTrack.ReadRTP()
	if err != nil {
		return err
	}

	return t.mediaSink.PushRTP(ptk)
}

func (t *whipTrackHandler) mediaWriterWorker(onDone func(err error)) {
	var err error

	defer func() {
		t.mediaSink.Close()
		if onDone != nil {
			onDone(err)
		}
	}()

	for {
		select {
		case s := <-t.samplesChan:
			err := t.mediaSink.PushSample(s.s, s.ts)
			switch err {
			case nil, errors.ErrPrerollBufferReset:
				// continue
			case io.EOF:
				err = nil //nolint
				return
			default:
				t.logger.Warnw("error writing media", err)
				return
			}
		case <-t.fuse.Watch():
			t.logger.Debugw("stopping media forwarder")
			err = nil
			return
		}
	}
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

func (t *whipTrackHandler) createDepacketizer() (rtp.Depacketizer, error) {
	var depacketizer rtp.Depacketizer

	switch strings.ToLower(t.remoteTrack.Codec().MimeType) {
	case strings.ToLower(webrtc.MimeTypeVP8):
		depacketizer = &codecs.VP8Packet{}

	case strings.ToLower(webrtc.MimeTypeH264):
		depacketizer = &codecs.H264Packet{}

	case strings.ToLower(webrtc.MimeTypeOpus):
		depacketizer = &codecs.OpusPacket{}

	default:
		return nil, errors.ErrUnsupportedDecodeMimeType(t.remoteTrack.Codec().MimeType)
	}

	return depacketizer, nil
}

func (t *whipTrackHandler) createJitterBuffer() (*jitter.Buffer, error) {
	var maxLatency time.Duration
	options := []jitter.Option{jitter.WithLogger(t.logger)}

	depacketizer, err := t.createDepacketizer()
	if err != nil {
		return nil, err
	}

	switch strings.ToLower(t.remoteTrack.Codec().MimeType) {
	case strings.ToLower(webrtc.MimeTypeVP8):
		maxLatency = maxVideoLatency
		options = append(options, jitter.WithPacketDroppedHandler(func() { t.writePLI(t.remoteTrack.SSRC()) }))

	case strings.ToLower(webrtc.MimeTypeH264):
		maxLatency = maxVideoLatency
		options = append(options, jitter.WithPacketDroppedHandler(func() { t.writePLI(t.remoteTrack.SSRC()) }))

	case strings.ToLower(webrtc.MimeTypeOpus):
		maxLatency = maxAudioLatency
		// No PLI for audio

	default:
		return nil, errors.ErrUnsupportedDecodeMimeType(t.remoteTrack.Codec().MimeType)
	}

	clockRate := t.remoteTrack.Codec().ClockRate

	jb := jitter.NewBuffer(depacketizer, clockRate, maxLatency, options...)

	return jb, nil
}
