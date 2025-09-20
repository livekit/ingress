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
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/utils"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type SDKWhipTrackHandler struct {
	logger           logger.Logger
	remoteTrack      *webrtc.TrackRemote
	quality          livekit.VideoQuality
	receiver         *webrtc.RTPReceiver
	writePLI         func(ssrc webrtc.SSRC)
	sendRTCPUpStream func(pkt rtcp.Packet)

	startRTCP   sync.Once
	fuse        core.Fuse
	lastSn      uint16
	lastSnValid bool

	stateLock      sync.Mutex
	trackMediaSink *SDKMediaSinkTrack
	trackStats     *stats.MediaTrackStatGatherer
	stats          *stats.LocalMediaStatsGatherer
}

func NewSDKWhipTrackHandler(
	logger logger.Logger,
	track *webrtc.TrackRemote,
	quality livekit.VideoQuality,
	receiver *webrtc.RTPReceiver,
	writePLI func(ssrc webrtc.SSRC),
	sendRTCPUpStream func(pkt rtcp.Packet),
) (*SDKWhipTrackHandler, error) {

	return &SDKWhipTrackHandler{
		logger:           logger,
		remoteTrack:      track,
		quality:          quality,
		receiver:         receiver,
		writePLI:         writePLI,
		sendRTCPUpStream: sendRTCPUpStream,
	}, nil
}

func (t *SDKWhipTrackHandler) Start(onDone func(err error)) (err error) {
	t.startRTPReceiver(onDone)
	t.startRTCP.Do(t.startRTCPReceiver)

	return nil
}

func (t *SDKWhipTrackHandler) SetMediaTrackStatsGatherer(st *stats.LocalMediaStatsGatherer) {
	t.stateLock.Lock()

	t.stats = st

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

	if t.trackMediaSink != nil {
		t.trackMediaSink.SetStatsGatherer(st)
	}
	t.stateLock.Unlock()
}

func (t *SDKWhipTrackHandler) SetMediaSink(s *SDKMediaSinkTrack) {
	t.stateLock.Lock()
	t.trackMediaSink = s

	if t.stats != nil {
		t.trackMediaSink.SetStatsGatherer(t.stats)
	}

	t.trackMediaSink.SetRTCPPacketSink(t.handleRTCPPacket)

	t.stateLock.Unlock()
}

func (t *SDKWhipTrackHandler) Close() {
	t.fuse.Break()
}

func (t *SDKWhipTrackHandler) handleRTCPPacket(pkt rtcp.Packet) {
	// LK SDK -> WHIP RTCP handling

	if t.sendRTCPUpStream != nil {
		utils.ReplaceRTCPPacketSSRC(pkt, uint32(t.remoteTrack.SSRC()))

		t.sendRTCPUpStream(pkt)
	}
}

func (t *SDKWhipTrackHandler) startRTPReceiver(onDone func(err error)) {
	t.stateLock.Lock()
	trackMediaSink := t.trackMediaSink
	t.stateLock.Unlock()

	go func() {
		var err error

		defer func() {
			trackMediaSink.Close()
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
				err = t.processRTPPacket(trackMediaSink)
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

func (t *SDKWhipTrackHandler) startRTCPReceiver() {
	go func() {
		t.logger.Infow("starting SDK source rtcp receiver")

		for {
			select {
			case <-t.fuse.Watch():
				t.logger.Debugw("stopping SDK source rtcp receiver")
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

				t.onUpStreamRTCP(pkts)
			}
		}
	}()
}
func (t *SDKWhipTrackHandler) processRTPPacket(trackMediaSink *SDKMediaSinkTrack) error {
	var pkt *rtp.Packet

	_ = t.remoteTrack.SetReadDeadline(time.Now().Add(time.Millisecond * 500))

	pkt, _, err := t.remoteTrack.ReadRTP()
	if err != nil {
		return err
	}

	return t.pushRTP(pkt, trackMediaSink)
}

func (t *SDKWhipTrackHandler) pushRTP(pkt *rtp.Packet, trackMediaSink *SDKMediaSinkTrack) error {
	t.stateLock.Lock()
	stats := t.trackStats
	t.stateLock.Unlock()

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

	if stats != nil {
		stats.MediaReceived(int64(len(pkt.Payload)))
	}

	err := trackMediaSink.PushRTP(pkt)
	if err != nil {
		return err
	}

	return nil
}

func (t *SDKWhipTrackHandler) onUpStreamRTCP(pkts []rtcp.Packet) {
	// WHIP -> LK SDK RTCP handling

	t.stateLock.Lock()
	trackMediaSink := t.trackMediaSink
	t.stateLock.Unlock()

	if trackMediaSink == nil {
		return
	}

	err := trackMediaSink.PushRTCP(pkts)
	if err != nil {
		t.logger.Infow("failed writing upstream WHIP RTCP packets", "error", err)
		return
	}
}
