package media

import (
	"fmt"

	ingress "github.com/livekit/livekit-ingress/pkg/proto"
	"github.com/livekit/protocol/livekit"
	"github.com/tinyzimmer/go-gst/gst"
)

// Encoder manages GStreamer elements that converts & encodes video to the specification that's
// suitable for WebRTC
type Encoder struct {
	bin      *gst.Bin
	elements []*gst.Element
}

func NewVideoEncoder(mimeType string, layer *livekit.VideoLayer) (*Encoder, error) {
	e := &Encoder{}

	videoConvert, err := gst.NewElement("videoconvert")
	if err != nil {
		return nil, err
	}
	videoScale, err := gst.NewElement("videoscale")
	if err != nil {
		return nil, err
	}
	inputCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		return nil, err
	}
	err = inputCaps.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf(
			"video/x-raw,framerate=%d/1,width=%d,height=%d",
			30, // TODO: get actual framerate
			layer.Width,
			layer.Height,
		),
	))
	if err != nil {
		return nil, err
	}
	e.elements = []*gst.Element{
		videoConvert, videoScale, inputCaps,
	}

	var enc *gst.Element
	switch mimeType {
	case "video/h264":
		enc, err = gst.NewElement("x264enc")
		if err != nil {
			return nil, err
		}
		if err = enc.SetProperty("bitrate", uint(layer.Bitrate)); err != nil {
			return nil, err
		}
		// temporary, only while during testing
		if err = enc.SetProperty("key-int-max", uint(100)); err != nil {
			return nil, err
		}
		enc.SetArg("speed-preset", "veryfast")
		enc.SetArg("tune", "zerolatency")
		profileCaps, err := gst.NewElement("capsfilter")
		if err != nil {
			return nil, err
		}
		err = profileCaps.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf("video/x-h264,profile=baseline"),
		))
		if err != nil {
			return nil, err
		}
		e.elements = append(e.elements, enc, profileCaps)
	case "video/vp8":
		enc, err = gst.NewElement("vp8enc")
		if err != nil {
			return nil, err
		}
		if err = enc.SetProperty("target-bitrate", int(layer.Bitrate)); err != nil {
			return nil, err
		}
		if err = enc.SetProperty("target-bitrate", int(layer.Bitrate)); err != nil {
			return nil, err
		}
		if err = enc.SetProperty("keyframe-max-dist", 100); err != nil {
			return nil, err
		}
		e.elements = append(e.elements, enc)
	default:
		return nil, ErrUnsupportedEncodeFormat
	}

	if err = gst.ElementLinkMany(e.elements...); err != nil {
		return nil, err
	}
	e.bin = gst.NewBin("encoder")

	if err = e.createBin(); err != nil {
		return nil, err
	}

	return nil, nil
}

func NewAudioEncoder(options *ingress.IngressAudioOptions) (*Encoder, error) {
	e := &Encoder{}

	audioConvert, err := gst.NewElement("audioconvert")
	if err != nil {
		return nil, err
	}

	channels := 2
	if options.Channels != 0 {
		channels = int(options.Channels)
	}

	capsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		return nil, err
	}
	err = capsFilter.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("audio/x-raw,format=S16LE,layout=interleaved,rate=48000,channels=%d", channels),
	))
	if err != nil {
		return nil, err
	}

	var enc *gst.Element
	switch options.MimeType {
	case "audio/opus":
		enc, err = gst.NewElement("opusenc")
		if err != nil {
			return nil, err
		}
		if err = enc.SetProperty("bitrate", int(options.Bitrate)); err != nil {
			return nil, err
		}
		if err = enc.SetProperty("dtx", options.Dtx); err != nil {
			return nil, err
		}
	default:
		return nil, ErrUnsupportedEncodeFormat
	}

	e.elements = []*gst.Element{
		audioConvert, capsFilter, enc,
	}
	if err = gst.ElementLinkMany(e.elements...); err != nil {
		return nil, err
	}

	if err = e.createBin(); err != nil {
		return nil, err
	}

	return e, nil
}

func (e *Encoder) createBin() error {
	e.bin = gst.NewBin("encoder")
	if err := e.bin.AddMany(e.elements...); err != nil {
		return err
	}
	binSink := gst.NewGhostPad("sink", e.elements[0].GetStaticPad("sink"))
	binSrc := gst.NewGhostPad("src", e.elements[len(e.elements)-1].GetStaticPad("src"))
	if !e.bin.AddPad(binSink.Pad) || !e.bin.AddPad(binSrc.Pad) {
		return ErrUnableToAddPad
	}
	return nil
}

func (e *Encoder) Bin() *gst.Bin {
	return e.bin
}
