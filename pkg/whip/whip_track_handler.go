package whip

import (
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/server-sdk-go/pkg/samplebuilder"
	"github.com/livekit/server-sdk-go/pkg/synchronizer"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

const (
	maxVideoLate = 300 // nearly 2s for fhd video
	maxAudioLate = 25  // 4s for audio
)

type MediaSink interface {
	PushSample(s *media.Sample, ts time.Duration) error
	Close()
}

type whipTrackHandler struct {
	logger      logger.Logger
	remoteTrack *webrtc.TrackRemote
	receiver    *webrtc.RTPReceiver
	sb          *samplebuilder.SampleBuilder
	mediaSink   MediaSink
	sync        *synchronizer.TrackSynchronizer
	writePLI    func(ssrc webrtc.SSRC)
	onRTCP      func(packet rtcp.Packet)

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
) (*whipTrackHandler, error) {
	logger = logger.WithValues("trackID", t.remoteTrack.ID(), "kind", t.remoteTrack.Kind())

	t := &whipTrackHandler{
		logger:      logger,
		remoteTrack: track,
		receiver:    receiver,
		sync:        sync,
		mediaSink:   mediaSink,
		writePLI:    writePLI,
		onRTCP:      onRTCP,
		fuse:        core.NewFuse(),
	}

	sb, err := t.createSampleBuildler()
	if err != nil {
		return nil, err
	}
	t.sb = sb

	return t, nil
}

func (t *whipTrackHandler) Start(onDone func(err error)) (err error) {
	t.startRTPReceiver(onDone)
	if t.onRTCP != nil {
		t.startRTCPReceiver()
	}

	return nil
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
				case nil, errors.ErrPrerollBufferReset:
					// continue
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

// TODO drain on close?
func (t *whipTrackHandler) processRTPPacket() error {
	var pkt *rtp.Packet

	_ = t.remoteTrack.SetReadDeadline(time.Now().Add(time.Millisecond * 500))

	pkt, _, err := t.remoteTrack.ReadRTP()
	if err != nil {
		return err
	}

	t.firstPacket.Do(func() {
		t.logger.Debugw("first packet received")
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

		err = t.mediaSink.PushSample(s, ts)
		if err != nil {
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
					err = nil
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

func (t *whipTrackHandler) createSampleBuildler() (*MediaSink, error) {
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

	sb := samplebuilder.New(
		maxLate, depacketizer, t.remoteTrack.Codec().ClockRate,
		samplebuilder.WithPacketDroppedHandler(writePLI),
	)

	return sb, nil
}
