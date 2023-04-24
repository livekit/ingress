package whip

import (
	"fmt"
	"io"
	"net"
	"time"

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/server-sdk-go/pkg/samplebuilder"
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
	appSrc      *app.Source
	sb          *samplebuilder.SampleBuilder
	writePLI    func(ssrc webrtc.SSRC)

	fuse   core.Fuse
	result chan error
}

func NewWHIPAppSource(remoteTrack *webrtc.TrackRemote, writePLI func(ssrc webrtc.SSRC)) (*WHIPAppSource, error) {
	w := &WHIPAppSource{
		remoteTrack: remoteTrack,
		writePLI:    writePLI,
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

	return nil
}

func (w *WHIPAppSource) Close() error {
	w.fuse.Break()

	return <-w.result
}

// TODO drain on close?
func (w *WHIPAppSource) processRTPPacket() error {
	var p *rtp.Packet

	_ = w.remoteTrack.SetReadDeadline(time.Now().Add(time.Millisecond * 500))

	p, _, err := w.remoteTrack.ReadRTP()
	if err != nil {
		return err
	}

	w.sb.Push(p)
	for {
		s, rtpTs := w.sb.PopWithTimestamp()
		if s == nil {
			break
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

func (w *WHIPAppSource) createSampleBuilder() (*samplebuilder.SampleBuilder, error) {
	var depacketizer rtp.Depacketizer
	var maxLate uint16
	var writePLI func()

	switch w.remoteTrack.Codec().MimeType {
	case webrtc.MimeTypeVP8:
		depacketizer = &codecs.VP8Packet{}
		maxLate = maxVideoLate
		writePLI = func() { w.writePLI(w.remoteTrack.SSRC()) }

	case webrtc.MimeTypeH264:
		depacketizer = &codecs.H264Packet{}
		maxLate = maxVideoLate
		writePLI = func() { w.writePLI(w.remoteTrack.SSRC()) }

	case webrtc.MimeTypeOpus:
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
	switch mimeType {
	case webrtc.MimeTypeH264:
		return gst.NewCapsFromString("video/x-h264,stream-format=byte-stream,alignment=au"), nil
	case webrtc.MimeTypeVP8:
		return gst.NewCapsFromString("video/x-vp8"), nil
	case webrtc.MimeTypeOpus:
		return gst.NewCapsFromString("audio/x-opus"), nil
	}

	return nil, errors.ErrUnsupportedDecodeFormat
}
