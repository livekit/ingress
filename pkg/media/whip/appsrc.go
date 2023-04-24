package whip

import (
	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/logger"
	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
)

// TODO Set caps

type WhipAppSource struct {
	remoteTrack *webrtc.TrackRemote
	appSrc      *app.Source

	fuse *core.Fuse
}

func NewWhipAppSource(remoteTrack *webrtc.TrackRemote) (*WhipAppSource, error) {
	w := &WhipAppSource{
		remoteTrack: remoteTrack,
		fuse:        core.NewFuse(),
	}

	elem, err := gst.NewElementWithName("appsrc", FlvAppSource)
	if err != nil {
		logger.Errorw("could not create appsrc", err)
		return nil, err
	}
	if err = elem.SetProperty("caps", caps); err != nil {
		return nil, err
	}
	if err = elem.SetProperty("is-live", true); err != nil {
		return nil, err
	}
	elem.SetArg("format", "time")

	return w, nil
}

func getCapsForCodec(string mimeType) (*gst.Caps, error) {
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
