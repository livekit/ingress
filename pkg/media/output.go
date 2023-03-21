package media

import (
	"fmt"
	"io"

	"github.com/pion/webrtc/v3/pkg/media"
	"github.com/pion/webrtc/v3/pkg/media/h264reader"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

const (
	opusFrameSize = 20
)

// Output manages GStreamer elements that converts & encodes video to the specification that's
// suitable for WebRTC
type Output struct {
	bin *gst.Bin

	elements []*gst.Element
	enc      *gst.Element
	sink     *app.Sink

	samples chan *media.Sample
}

// FIXME Use generics instead?
type VideoOutput struct {
	*Output

	codec livekit.VideoCodec
}

type AudioOutput struct {
	*Output

	codec livekit.AudioCodec
}

func NewVideoOutput(codec livekit.VideoCodec, layer *livekit.VideoLayer) (*VideoOutput, error) {
	e, err := newVideoOutput(codec)
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
			"video/x-raw,width=%d,height=%d",
			layer.Width,
			layer.Height,
		),
	))
	if err != nil {
		return nil, err
	}
	e.elements = []*gst.Element{
		videoScale, inputCaps,
	}

	switch codec {
	case livekit.VideoCodec_H264_BASELINE:
		e.enc, err = gst.NewElement("x264enc")
		if err != nil {
			return nil, err
		}

		if err = e.enc.SetProperty("bitrate", uint(layer.Bitrate/1000)); err != nil {
			return nil, err
		}
		if err = e.enc.SetProperty("byte-stream", true); err != nil {
			return nil, err
		}
		e.enc.SetArg("speed-preset", "veryfast")

		profileCaps, err := gst.NewElement("capsfilter")
		if err != nil {
			return nil, err
		}
		if err = profileCaps.SetProperty("caps", gst.NewCapsFromString(
			fmt.Sprintf("video/x-h264,stream-format=byte-stream,profile=baseline"),
		)); err != nil {
			return nil, err
		}

		e.elements = append(e.elements, e.enc, profileCaps)
		if err != nil {
			return nil, err
		}

	case livekit.VideoCodec_VP8:
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

	queue, err := gst.NewElement("queue")
	if err != nil {
		return nil, err
	}
	if err = queue.SetProperty("max-size-time", uint64(3e9)); err != nil {
		return nil, err
	}

	e.elements = append(e.elements, queue, e.sink.Element)

	e.bin = gst.NewBin(fmt.Sprintf("video_%s", layer.Quality.String()))
	if err = e.linkElements(); err != nil {
		return nil, err
	}

	return e, nil
}

func NewAudioOutput(options *livekit.IngressAudioEncodingOptions) (*AudioOutput, error) {
	e, err := newAudioOutput(options.AudioCodec)
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

	audioResample, err := gst.NewElement("audioresample")
	if err != nil {
		return nil, err
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

	switch options.AudioCodec {
	case livekit.AudioCodec_OPUS:
		e.enc, err = gst.NewElement("opusenc")
		if err != nil {
			return nil, err
		}

		if options.Bitrate != 0 {
			if err = e.enc.SetProperty("bitrate", int(options.Bitrate)); err != nil {
				return nil, err
			}
		}
		if err = e.enc.SetProperty("dtx", !options.DisableDtx); err != nil {
			return nil, err
		}
		// TODO: FEC?
		// if err = e.enc.SetProperty("inband-fec", true); err != nil {
		// 	return nil, err
		// }
		e.enc.SetArg("frame-size", fmt.Sprint(opusFrameSize))

	default:
		return nil, errors.ErrUnsupportedEncodeFormat
	}

	queue, err := gst.NewElement("queue")
	if err != nil {
		return nil, err
	}
	if err = queue.SetProperty("max-size-time", uint64(3e9)); err != nil {
		return nil, err
	}

	e.elements = []*gst.Element{
		audioConvert, audioResample, capsFilter, e.enc, queue, e.sink.Element,
	}

	e.bin = gst.NewBin("audio")
	if err = e.linkElements(); err != nil {
		return nil, err
	}

	return e, nil
}

func newVideoOutput(codec livekit.VideoCodec) (*VideoOutput, error) {
	e, err := newOutput()
	if err != nil {
		return nil, err
	}

	o := &VideoOutput{
		Output: e,
		codec:  codec,
	}

	o.sink.SetCallbacks(&app.SinkCallbacks{
		EOSFunc:       o.handleEOS,
		NewSampleFunc: o.handleSample,
	})

	return o, nil
}

func newAudioOutput(codec livekit.AudioCodec) (*AudioOutput, error) {
	e, err := newOutput()
	if err != nil {
		return nil, err
	}

	o := &AudioOutput{
		Output: e,
		codec:  codec,
	}

	o.sink.SetCallbacks(&app.SinkCallbacks{
		EOSFunc:       o.handleEOS,
		NewSampleFunc: o.handleSample,
	})

	return o, nil
}

func newOutput() (*Output, error) {
	sink, err := app.NewAppSink()
	if err != nil {
		return nil, err
	}

	e := &Output{
		sink:    sink,
		samples: make(chan *media.Sample, 1000),
	}

	return e, nil
}

func (e *Output) linkElements() error {
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

func (e *Output) Bin() *gst.Bin {
	return e.bin
}

func (e *Output) ForceKeyFrame() error {
	keyFrame := gst.NewStructure("GstForceKeyUnit")
	if err := keyFrame.SetValue("all-headers", true); err != nil {
		return err
	}
	e.enc.SendEvent(gst.NewCustomEvent(gst.EventTypeCustomDownstream, keyFrame))
	return nil
}

func (e *Output) handleEOS(_ *app.Sink) {
	close(e.samples)
}

func (e *Output) writeSample(sample *media.Sample) {
	select {
	case e.samples <- sample:
		// continue
	default:
		logger.Warnw("sample channel full", nil)
		e.samples <- sample
	}
}

func (e *Output) NextSample() (media.Sample, error) {
	sample := <-e.samples
	if sample == nil {
		return media.Sample{}, io.EOF
	}

	return *sample, nil
}

func (e *Output) OnBind() error {
	return nil
}

func (e *Output) OnUnbind() error {
	return nil
}

func (e *VideoOutput) handleSample(sink *app.Sink) gst.FlowReturn {
	// Pull the sample that triggered this callback
	s := sink.PullSample()
	if s == nil {
		return gst.FlowEOS
	}

	// Retrieve the buffer from the sample
	buffer := s.GetBuffer()
	if buffer == nil {
		return gst.FlowError
	}

	duration := buffer.Duration()

	switch e.codec {
	case livekit.VideoCodec_H264_BASELINE:
		data := buffer.Bytes()

		var (
			currentNalType h264reader.NalUnitType
		)
		nalStart := -1
		zeroes := 0

	duration_loop:
		for i, b := range data {
			if i == nalStart {
				// get type of current NAL
				currentNalType = h264reader.NalUnitType(b & 0x1F)
				switch currentNalType {
				case h264reader.NalUnitTypeCodedSliceDataPartitionA,
					h264reader.NalUnitTypeCodedSliceDataPartitionB,
					h264reader.NalUnitTypeCodedSliceDataPartitionC,
					h264reader.NalUnitTypeCodedSliceIdr,
					h264reader.NalUnitTypeCodedSliceNonIdr:
					break duration_loop
				}
			}

			if b == 0 {
				zeroes++
			} else {
				// NAL separator is either [0 0 0 1] or [0 0 1]
				if b == 1 && (zeroes > 1) {

					nalStart = i + 1
				}

				zeroes = 0
			}
		}
		e.writeSample(&media.Sample{
			Data:     buffer.Bytes(),
			Duration: duration,
		})

	case livekit.VideoCodec_VP8:
		// untested
		e.writeSample(&media.Sample{
			Data:     buffer.Bytes(),
			Duration: duration,
		})
	}

	return gst.FlowOK
}

func (e *AudioOutput) handleSample(sink *app.Sink) gst.FlowReturn {
	// Pull the sample that triggered this callback
	s := sink.PullSample()
	if s == nil {
		return gst.FlowEOS
	}

	// Retrieve the buffer from the sample
	buffer := s.GetBuffer()
	if buffer == nil {
		return gst.FlowError
	}

	duration := buffer.Duration()

	switch e.codec {
	case livekit.AudioCodec_OPUS:
		e.writeSample(&media.Sample{
			Data:     buffer.Bytes(),
			Duration: duration,
		})
	}

	return gst.FlowOK
}
