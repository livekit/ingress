package whip

import (
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/utils"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/server-sdk-go/pkg/samplebuilder"
	"github.com/livekit/server-sdk-go/pkg/synchronizer"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
)

const (
	maxVideoLate = 300 // nearly 2s for fhd video
	maxAudioLate = 25  // 4s for audio
)

type whipTrackHandler struct {
	remoteTrack *webrtc.TrackRemote
	receiver    *webrtc.RTPReceiver
	sb          *samplebuilder.SampleBuilder
	sync        *synchronizer.TrackSynchronizer
	writePLI    func(ssrc webrtc.SSRC)
	onRTCP      func(packet rtcp.Packet)

	firstPacket sync.Once
	mediaBuffer *utils.PrerollBuffer
	fuse        core.Fuse
	result      chan error
}

func newWHIPTrackHandler(
	track *webrtc.TrackRemote,
	receiver *webrtc.RTPReceiver,
	sync *synchronizer.TrackSynchronizer,
	writePLI func(ssrc webrtc.SSRC),
	onRTCP func(packet rtcp.Packet),
) (*whipTrackHandler, error) {
	t := &whipTrackHandler{
		remoteTrack: track,
		receiver:    receiver,
		sync:        sync,
		writePLI:    writePLI,
		onRTCP:      onRTCP,
		fuse:        core.NewFuse(),
	}

	sb, err := t.createSampleBuilder()
	if err != nil {
		return nil, err
	}
	t.sb = sb

	t.mediaBuffer = utils.NewPrerollBuffer(func() error {
		logger.Infow("preroll buffer reset event", "trackID", t.remoteTrack.ID(), "kind", t.remoteTrack.Kind())

		return nil
	})

	return t, nil
}

func (t *whipTrackHandler) Start() (waitForDone func() error, err error) {
	t.result = make(chan error, 1)
	t.startRTPReceiver()
	if t.onRTCP != nil {
		t.startRTCPReceiver()
	}

	return func() error { return <-t.result }, nil
}

func (t *whipTrackHandler) Close() {
	t.fuse.Break()
}

func (t *whipTrackHandler) SetWriter(w io.WriteCloser) error {
	return t.mediaBuffer.SetWriter(w)
}

func (t *whipTrackHandler) startRTPReceiver() {
	go func() {
		var err error

		defer func() {
			t.mediaBuffer.Close()
			t.result <- err
			close(t.result)
		}()

		logger.Infow("starting rtp receiver", "trackID", t.remoteTrack.ID(), "kind", t.remoteTrack.Kind())

		if t.remoteTrack.Kind() == webrtc.RTPCodecTypeVideo && t.writePLI != nil {
			t.writePLI(t.remoteTrack.SSRC())
		}

		for {
			select {
			case <-t.fuse.Watch():
				logger.Debugw("stopping rtp receiver", "trackID", t.remoteTrack.ID(), "kind", t.remoteTrack.Kind())
				err = nil
				return
			default:
				err = t.processRTPPacket()
				switch err {
				case nil, errors.ErrPrerollBufferReset:
					// continue
				case io.EOF:
					err = nil
					return
				default:
					if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
						continue
					}

					logger.Warnw("error reading rtp packets", err, "trackID", t.remoteTrack.ID(), "kind", t.remoteTrack.Kind())
					return
				}
			}
		}
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

	t.firstPacket.Do(func() {
		logger.Debugw("first packet received", "trackID", t.remoteTrack.ID(), "kind", t.remoteTrack.Kind())
		t.sync.FirstPacketForTrack(pkt)
	})

	t.sb.Push(pkt)
	for {
		s, rtpTs := t.sb.PopWithTimestamp()
		if s == nil {
			break
		}

		ts, err := t.sync.GetPTS(rtpTs)
		if err != nil {
			return err
		}

		err = utils.SerializeMediaForRelay(t.mediaBuffer, s.Data, ts)
		if err != nil {
			return err
		}
	}

	return nil
}

func (t *whipTrackHandler) startRTCPReceiver() {
	go func() {
		logger.Infow("starting app source rtcp receiver", "trackID", t.remoteTrack.ID(), "kind", t.remoteTrack.Kind())

		for {
			select {
			case <-t.fuse.Watch():
				logger.Debugw("stopping app source rtcp receiver", "trackID", t.remoteTrack.ID(), "kind", t.remoteTrack.Kind())
				return
			default:
				_ = t.receiver.SetReadDeadline(time.Now().Add(time.Millisecond * 500))
				pkts, _, err := t.receiver.ReadRTCP()

				switch {
				case err == nil:
					// continue
				case err == io.EOF:
					err = nil
					return
				default:
					if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
						continue
					}

					logger.Warnw("error reading rtcp", err, "trackID", err, t.remoteTrack.ID(), "kind", t.remoteTrack.Kind())
					return
				}

				for _, pkt := range pkts {
					t.onRTCP(pkt)
				}
			}
		}
	}()
}

func (t *whipTrackHandler) createSampleBuilder() (*samplebuilder.SampleBuilder, error) {
	var depacketizer rtp.Depacketizer
	var maxLate uint16
	var writePLI func()

	switch strings.ToLower(t.remoteTrack.Codec().MimeType) {
	case strings.ToLower(webrtc.MimeTypeVP8):
		depacketizer = &codecs.VP8Packet{}
		maxLate = maxVideoLate
		writePLI = func() { t.writePLI(t.remoteTrack.SSRC()) }

	case strings.ToLower(webrtc.MimeTypeH264):
		depacketizer = &codecs.H264Packet{}
		maxLate = maxVideoLate
		writePLI = func() { t.writePLI(t.remoteTrack.SSRC()) }

	case strings.ToLower(webrtc.MimeTypeOpus):
		depacketizer = &codecs.OpusPacket{}
		maxLate = maxAudioLate
		// No PLI for audio

	default:
		return nil, errors.ErrUnsupportedDecodeFormat
	}

	return samplebuilder.New(
		maxLate, depacketizer, t.remoteTrack.Codec().ClockRate,
		samplebuilder.WithPacketDroppedHandler(writePLI),
	), nil
}
