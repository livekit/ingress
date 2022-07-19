package media

import (
	"fmt"
	"io"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"github.com/pion/webrtc/v3/pkg/media/h264reader"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

const (
	opusFrameSize       = 20 * time.Millisecond
	opusFrameSizeString = "20"
)

// Encoder manages GStreamer elements that converts & encodes video to the specification that's
// suitable for WebRTC
type Encoder struct {
	bin *gst.Bin

	mimeType string
	elements []*gst.Element
	enc      *gst.Element
	sink     *app.Sink

	samples chan *media.Sample
}

func NewVideoEncoder(mimeType string, layer *livekit.VideoLayer) (*Encoder, error) {
	e, err := newEncoder(mimeType)
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

	e.bin = gst.NewBin("video")
	if err = e.linkElements(); err != nil {
		return nil, err
	}

	return e, nil
}

func NewAudioEncoder(options *livekit.IngressAudioOptions) (*Encoder, error) {
	e, err := newEncoder(options.MimeType)
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

	switch options.MimeType {
	case webrtc.MimeTypeOpus:
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
		e.enc.SetArg("frame-size", opusFrameSizeString)

	default:
		return nil, errors.ErrUnsupportedEncodeFormat
	}

	e.elements = []*gst.Element{
		audioConvert, audioResample, capsFilter, e.enc, e.sink.Element,
	}

	e.bin = gst.NewBin("audio")
	if err = e.linkElements(); err != nil {
		return nil, err
	}

	return e, nil
}

func newEncoder(mimeType string) (*Encoder, error) {
	sink, err := app.NewAppSink()
	if err != nil {
		return nil, err
	}

	e := &Encoder{
		mimeType: mimeType,
		sink:     sink,
		samples:  make(chan *media.Sample, 1000),
	}

	sink.SetCallbacks(&app.SinkCallbacks{
		EOSFunc:       e.handleEOS,
		NewSampleFunc: e.handleSample,
	})

	return e, nil
}

func (e *Encoder) handleEOS(_ *app.Sink) {
	close(e.samples)
}

func (e *Encoder) handleSample(sink *app.Sink) gst.FlowReturn {
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

	switch e.mimeType {
	case webrtc.MimeTypeH264:
		data := buffer.Bytes()

		var currentNalType h264reader.NalUnitType
		nalStart := -1
		zeroes := 0

		// pion's h264 packetizer only accepts one NAL per packet
		for i, b := range data {
			if i == nalStart {
				// get type of current NAL
				currentNalType = h264reader.NalUnitType((b & 0x1F) >> 0)
				if currentNalType == h264reader.NalUnitTypeSEI {
					nalStart = -1
				}
			}

			if b == 0 {
				zeroes++
			} else {
				// NAL separator is either [0 0 0 1] or [0 0 1]
				if b == 1 && (zeroes > 1) {
					if nalStart > 0 {
						nalEnd := i - zeroes
						if zeroes > 3 {
							nalEnd = i - 3
						}

						e.writeNal(data[nalStart:nalEnd], currentNalType)
					}

					nalStart = i + 1
				}

				zeroes = 0
			}
		}

		if nalStart > 0 && nalStart < len(data) {
			e.writeNal(data[nalStart:], currentNalType)
		}

	case webrtc.MimeTypeVP8:
		// untested
		e.writeSample(&media.Sample{
			Data:     buffer.Bytes(),
			Duration: time.Second / 30.0,
		})

	case webrtc.MimeTypeOpus:
		e.writeSample(&media.Sample{
			Data:     buffer.Bytes(),
			Duration: opusFrameSize,
		})
	}

	return gst.FlowOK
}

func (e *Encoder) writeNal(nal []byte, nalType h264reader.NalUnitType) {
	sample := &media.Sample{
		Data: nal,
	}

	// only these NAL types get a duration
	switch nalType {
	case h264reader.NalUnitTypeCodedSliceDataPartitionA,
		h264reader.NalUnitTypeCodedSliceDataPartitionB,
		h264reader.NalUnitTypeCodedSliceDataPartitionC,
		h264reader.NalUnitTypeCodedSliceIdr,
		h264reader.NalUnitTypeCodedSliceNonIdr:
		sample.Duration = time.Second / 30.0
	}

	e.writeSample(sample)
}

func (e *Encoder) writeSample(sample *media.Sample) {
	select {
	case e.samples <- sample:
		// continue
	default:
		logger.Infow("sample channel full")
		e.samples <- sample
	}
}

func (e *Encoder) linkElements() error {
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

func (e *Encoder) NextSample() (media.Sample, error) {
	sample := <-e.samples
	if sample == nil {
		return media.Sample{}, io.EOF
	}

	return *sample, nil
}

func (e *Encoder) OnBind() error {
	return nil
}

func (e *Encoder) OnUnbind() error {
	return nil
}
