package whip

import (
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/logger"
	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
)

type WHIPAppSource struct {
	appSrc    *app.Source
	trackKind types.StreamKind

	fuse   core.Fuse
	result chan error
}

func newWHIPAppSource(trackKind types.StreamKind) (*WHIPAppSource, error) {
	w := &WHIPAppSource{
		trackKind: trackKind,
	}

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
	return nil
}

func (w *WHIPAppSource) Close() error {
	w.fuse.Break()

	return <-w.result
}

func (w *WHIPAppSource) GetAppSource() *app.Source {
	return w.appSrc
}

func (w *WHIPAppSource) startTrackRelay() {
	go func() {
		var err error

		defer func() {
			w.appSrc.EndStream()

			w.result <- err
			close(w.result)
		}()

		logger.Infow("starting app source track reader", "kind", w.trackKind)

		if w.remoteTrack.Kind() == webrtc.RTPCodecTypeVideo && w.writePLI != nil {
			w.writePLI(w.remoteTrack.SSRC())
		}

		for {
			select {
			case <-w.fuse.Watch():
				logger.Debugw("stopping app source track reader", "kind", w.trackKind)
				return
			default:
				err = w.relayRTPPacket()
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

					logger.Warnw("error reading track", err, "kind", w.trackKind)
					return
				}
			}
		}

	}()
}

func relayRTPPacket() {
	b := gst.NewBufferFromBytes(s.Data)
	b.SetPresentationTimestamp(ts)

	ret := t.appSrc.PushBuffer(b)
	switch ret {
	case gst.FlowOK, gst.FlowFlushing:
		// continue
	case gst.FlowEOS:
		t.Close()
		return io.EOF
	default:
		t.Close()
		return errors.ErrFromGstFlowReturn(ret)
	}

}

func getCapsForCodec(mimeType string) (*gst.Caps, error) {
	mt := strings.ToLower(mimeType)

	switch mt {
	case strings.ToLower(webrtc.MimeTypeH264):
		return gst.NewCapsFromString("video/x-h264,stream-format=byte-stream,alignment=nal"), nil
	case strings.ToLower(webrtc.MimeTypeVP8):
		return gst.NewCapsFromString("video/x-vp8"), nil
	case strings.ToLower(webrtc.MimeTypeOpus):
		return gst.NewCapsFromString("audio/x-opus,channel-mapping-family=0"), nil
	}

	return nil, errors.ErrUnsupportedDecodeFormat
}
