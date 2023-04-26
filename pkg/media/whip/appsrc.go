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
	"github.com/livekit/protocol/logger"
	"github.com/livekit/server-sdk-go/pkg/samplebuilder"
	"github.com/livekit/server-sdk-go/pkg/synchronizer"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
)

// TODO values taken from egress
const (
	maxVideoLate = 1000 // nearly 2s for fhd video
	maxAudioLate = 200  // 4s for audio
	maxDropout   = 3000 // max sequence number skip
)

type WHIPAppSource struct {
	remoteTrack *webrtc.TrackRemote
	receiver    *webrtc.RTPReceiver
	appSrc      *app.Source
	sb          *samplebuilder.SampleBuilder
	sync        *synchronizer.TrackSynchronizer
	writePLI    func(ssrc webrtc.SSRC)
	onRTCP      func(packet rtcp.Packet)

	firstPacket sync.Once
	fuse        core.Fuse
	result      chan error
}

func NewWHIPAppSource(
	remoteTrack *webrtc.TrackRemote,
	receiver *webrtc.RTPReceiver,
	sync *synchronizer.TrackSynchronizer,
	writePLI func(ssrc webrtc.SSRC),
	onRTCP func(packet rtcp.Packet),
) (*WHIPAppSource, error) {
	w := &WHIPAppSource{
		remoteTrack: remoteTrack,
		receiver:    receiver,
		sync:        sync,
		writePLI:    writePLI,
		onRTCP:      onRTCP,
		fuse:        core.NewFuse(),
	}

	sb, err := w.createSampleBuilder()
	if err != nil {
		return nil, err
	}
	w.sb = sb

	elem, err := gst.NewElementWithName("appsrc", fmt.Sprintf("%s_%s", WHIPAppSourceLabel, remoteTrack.Kind()))
	if err != nil {
		logger.Errorw("could not create appsrc", err)
		return nil, err
	}
	caps, err := getCapsForCodec(remoteTrack.Codec().MimeType)
	if err != nil {
		return nil, err
	}
	if err = elem.SetProperty("caps", caps); err != nil {
		return nil, err
	}
	if err = elem.SetProperty("is-live", true); err != nil {
		return nil, err
	}
	elem.SetArg("format", "time")

	w.appSrc = app.SrcFromElement(elem)

	return w, nil
}

func (w *WHIPAppSource) Start() error {
	w.result = make(chan error, 1)
	w.startRTPReceiver()
	if w.onRTCP != nil {
		w.startRTCPReceiver()
	}

	return nil
}

func (w *WHIPAppSource) Close() error {
	w.fuse.Break()

	return <-w.result
}

func (w *WHIPAppSource) GetAppSource() *app.Source {
	return w.appSrc
}

func (w *WHIPAppSource) startRTPReceiver() {
	go func() {
		var err error

		defer func() {
			w.appSrc.EndStream()

			w.result <- err
			close(w.result)
		}()

		logger.Infow("starting app source track reader", "trackID", w.remoteTrack.ID(), "kind", w.remoteTrack.Kind())

		for {
			select {
			case <-w.fuse.Watch():
				logger.Debugw("stopping app source track reader", "trackID", w.remoteTrack.ID(), "kind", w.remoteTrack.Kind())
			default:
				err = w.processRTPPacket()
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

					logger.Warnw("error reading track", err, "trackID", err, w.remoteTrack.ID(), "kind", w.remoteTrack.Kind())
					return
				}
			}
		}

	}()
}

// TODO drain on close?
func (w *WHIPAppSource) processRTPPacket() error {
	var pkt *rtp.Packet

	_ = w.remoteTrack.SetReadDeadline(time.Now().Add(time.Millisecond * 500))

	pkt, _, err := w.remoteTrack.ReadRTP()
	if err != nil {
		return err
	}

	w.firstPacket.Do(func() {
		w.sync.FirstPacketForTrack(pkt)
	})

	w.sb.Push(pkt)
	for {
		s, rtpTs := w.sb.PopWithTimestamp()
		if s == nil {
			break
		}

		ts, err := w.sync.GetPTS(rtpTs)
		if err != nil {
			return err
		}

		b := gst.NewBufferFromBytes(s.Data)
		b.SetPresentationTimestamp(ts)

		ret := w.appSrc.PushBuffer(b)
		switch ret {
		case gst.FlowOK, gst.FlowFlushing:
			// continue
		case gst.FlowEOS:
			w.Close()
			return io.EOF
		default:
			return errors.ErrFromGstFlowReturn(ret)
		}
	}

	return nil
}

func (w *WHIPAppSource) startRTCPReceiver() {
	go func() {
		logger.Infow("starting app source rtcp receiver", "trackID", w.remoteTrack.ID(), "kind", w.remoteTrack.Kind())

		for {
			select {
			case <-w.fuse.Watch():
				logger.Debugw("stopping app source rtcp receiver", "trackID", w.remoteTrack.ID(), "kind", w.remoteTrack.Kind())
			default:
				_ = w.receiver.SetReadDeadline(time.Now().Add(time.Millisecond * 500))
				pkts, _, err := w.receiver.ReadRTCP()

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

					logger.Warnw("error reading rtcp", err, "trackID", err, w.remoteTrack.ID(), "kind", w.remoteTrack.Kind())
					return
				}

				for _, pkt := range pkts {
					w.onRTCP(pkt)
				}
			}
		}

	}()
}

func (w *WHIPAppSource) createSampleBuilder() (*samplebuilder.SampleBuilder, error) {
	var depacketizer rtp.Depacketizer
	var maxLate uint16
	var writePLI func()

	switch strings.ToLower(w.remoteTrack.Codec().MimeType) {
	case strings.ToLower(webrtc.MimeTypeVP8):
		depacketizer = &codecs.VP8Packet{}
		maxLate = maxVideoLate
		writePLI = func() { w.writePLI(w.remoteTrack.SSRC()) }

	case strings.ToLower(webrtc.MimeTypeH264):
		depacketizer = &codecs.H264Packet{}
		maxLate = maxVideoLate
		writePLI = func() { w.writePLI(w.remoteTrack.SSRC()) }

	case strings.ToLower(webrtc.MimeTypeOpus):
		depacketizer = &codecs.OpusPacket{}
		maxLate = maxAudioLate
		// No PLI for audio

	default:
		return nil, errors.ErrUnsupportedDecodeFormat
	}

	return samplebuilder.New(
		maxLate, depacketizer, w.remoteTrack.Codec().ClockRate,
		samplebuilder.WithPacketDroppedHandler(writePLI),
	), nil
}
func getCapsForCodec(mimeType string) (*gst.Caps, error) {
	mt := strings.ToLower(mimeType)

	switch mt {
	case strings.ToLower(webrtc.MimeTypeH264):
		return gst.NewCapsFromString("video/x-h264,stream-format=byte-stream,alignment=au"), nil
	case strings.ToLower(webrtc.MimeTypeVP8):
		return gst.NewCapsFromString("video/x-vp8"), nil
	case strings.ToLower(webrtc.MimeTypeOpus):
		return gst.NewCapsFromString("audio/x-opus"), nil
	}

	return nil, errors.ErrUnsupportedDecodeFormat
}
