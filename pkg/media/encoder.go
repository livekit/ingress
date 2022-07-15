package media

import (
	"fmt"
	"io"

	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/livekit"
)

// Encoder manages GStreamer elements that converts & encodes video to the specification that's
// suitable for WebRTC
type Encoder struct {
	bin *gst.Bin

	elements []*gst.Element
	enc      *gst.Element
	sink     *app.Sink
	writer   *io.PipeWriter
	reader   *io.PipeReader
}

func NewVideoEncoder(mimeType string, layer *livekit.VideoLayer) (*Encoder, error) {
	e, err := newEncoder()
	if err != nil {
		return nil, err
	}

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

	switch mimeType {
	case webrtc.MimeTypeH264:
		e.enc, err = gst.NewElement("x264enc")
		if err != nil {
			return nil, err
		}
		if err = e.enc.SetProperty("bitrate", uint(layer.Bitrate)); err != nil {
			return nil, err
		}
		// temporary, only while during testing
		// if err = enc.SetProperty("key-int-max", uint(100)); err != nil {
		// 	return nil, err
		// }
		if err = e.enc.SetProperty("byte-stream", true); err != nil {
			return nil, err
		}
		e.enc.SetArg("speed-preset", "veryfast")
		e.enc.SetArg("tune", "zerolatency")
		profileCaps, err := gst.NewElement("capsfilter")
		if err != nil {
			return nil, err
		}
		err = profileCaps.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf("video/x-h264,stream-format=byte-stream,profile=baseline"),
		))
		if err != nil {
			return nil, err
		}
		e.elements = append(e.elements, e.enc, profileCaps)

	case webrtc.MimeTypeVP8:
		e.enc, err = gst.NewElement("vp8enc")
		if err != nil {
			return nil, err
		}
		if err = e.enc.SetProperty("target-bitrate", int(layer.Bitrate)); err != nil {
			return nil, err
		}
		if err = e.enc.SetProperty("keyframe-max-dist", 100); err != nil {
			return nil, err
		}
		e.elements = append(e.elements, e.enc)

	default:
		return nil, errors.ErrUnsupportedEncodeFormat
	}

	e.elements = append(e.elements, e.sink.Element)

	if err = e.linkElements(); err != nil {
		return nil, err
	}

	return e, nil
}

func NewAudioEncoder(options *livekit.IngressAudioOptions) (*Encoder, error) {
	e, err := newEncoder()
	if err != nil {
		return nil, err
	}

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
	case webrtc.MimeTypeOpus:
		enc, err = gst.NewElement("opusenc")
		if err != nil {
			return nil, err
		}
		if err = enc.SetProperty("bitrate", int(options.Bitrate)); err != nil {
			return nil, err
		}
		if err = enc.SetProperty("dtx", !options.DisableDtx); err != nil {
			return nil, err
		}

	default:
		return nil, errors.ErrUnsupportedEncodeFormat
	}

	e.elements = []*gst.Element{
		audioConvert, capsFilter, enc, e.sink.Element,
	}

	if err = e.linkElements(); err != nil {
		return nil, err
	}

	return e, nil
}

func newEncoder() (*Encoder, error) {
	sink, err := app.NewAppSink()
	if err != nil {
		return nil, err
	}

	e := &Encoder{
		sink: sink,
	}
	sink.SetCallbacks(&app.SinkCallbacks{
		EOSFunc:        e.handleEOS,
		NewPrerollFunc: nil,
		NewSampleFunc:  e.handleSample,
	})
	e.reader, e.writer = io.Pipe()
	return e, nil
}

func (e *Encoder) handleEOS(_ *app.Sink) {
	_ = e.Close()
}

func (e *Encoder) handleSample(sink *app.Sink) gst.FlowReturn {
	// Pull the sample that triggered this callback
	sample := sink.PullSample()
	if sample == nil {
		return gst.FlowEOS
	}

	// Retrieve the buffer from the sample
	buffer := sample.GetBuffer()
	if buffer == nil {
		return gst.FlowError
	}

	if _, err := e.writer.Write(buffer.Bytes()); err != nil {
		_ = e.writer.CloseWithError(err)
		return gst.FlowError
	}

	return gst.FlowOK
}

func (e *Encoder) linkElements() error {
	if e.bin != nil {
		// already linked
		return nil
	}

	e.bin = gst.NewBin("encoder")

	if err := e.bin.AddMany(e.elements...); err != nil {
		return err
	}
	if err := gst.ElementLinkMany(e.elements...); err != nil {
		return err
	}

	binSink := gst.NewGhostPad("sink", e.elements[0].GetStaticPad("sink"))
	if !e.bin.AddPad(binSink.Pad) {
		return errors.ErrUnableToAddPad
	}
	return nil
}

func (e *Encoder) Bin() *gst.Bin {
	return e.bin
}

func (e *Encoder) ForceKeyFrame() error {
	keyFrame := gst.NewStructure("GstForceKeyUnit")
	if err := keyFrame.SetValue("all-headers", true); err != nil {
		return err
	}
	e.enc.SendEvent(gst.NewCustomEvent(gst.EventTypeCustomDownstream, keyFrame))
	return nil
}

func (e *Encoder) Read(p []byte) (int, error) {
	return e.reader.Read(p)
}

func (e *Encoder) Close() error {
	return e.writer.Close()
}
