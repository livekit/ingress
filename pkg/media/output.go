// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package media

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/lksdk_output"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/utils"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/psrpc"
)

const (
	opusFrameSize = 20

	pixelsPerEncoderThread = 640 * 480

	queueCapacity         = 5
	latencySampleInterval = 500 * time.Millisecond
	eosQueueDrainTimeout  = 2 * time.Second
)

// Output manages GStreamer elements that converts & encodes video to the specification that's
// suitable for WebRTC
type Output struct {
	bin    *gst.Bin
	logger logger.Logger

	elements           []*gst.Element
	enc                *gst.Element
	sink               *app.Sink
	outputSync         *utils.TrackOutputSynchronizer
	isPlayingTooSlow   func() bool
	trackStatsGatherer *stats.MediaTrackStatGatherer
	queue              *utils.BlockingQueue[*sample]
	eos                *eosDispatcher

	localTrack   atomic.Pointer[lksdk_output.LocalTrack]
	stopDropping func()

	closed           core.Fuse
	pipelineErr      atomic.Pointer[error]
	latencyCaps      *gst.Caps
	latencySampledAt time.Time
}

type sample struct {
	s  *media.Sample
	ts time.Duration
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

func NewVideoOutput(codec livekit.VideoCodec, layer *livekit.VideoLayer, outputSync *utils.TrackOutputSynchronizer, isPlayingTooSlow func() bool, statsGatherer *stats.LocalMediaStatsGatherer, eos *eosDispatcher) (*VideoOutput, error) {
	e, err := newVideoOutput(codec, outputSync, isPlayingTooSlow, eos)
	if err != nil {
		return nil, err
	}

	e.logger = logger.GetLogger().WithValues("kind", "video", "layer", layer.Quality.String())

	e.trackStatsGatherer = statsGatherer.RegisterTrackStats(fmt.Sprintf("%s.%s", stats.OutputVideo, layer.Quality.String()))

	e.latencyCaps = gst.NewCapsFromString(packetLatencyCaps)

	threadCount := getVideoEncoderThreadCount(layer)

	e.logger.Infow("video layer", "width", layer.Width, "height", layer.Height, "threads", threadCount)

	queueIn, err := gst.NewElementWithName("queue", fmt.Sprintf("video_%s_in", layer.Quality.String()))
	if err != nil {
		return nil, err
	}
	if err = queueIn.SetProperty("max-size-buffers", uint(1)); err != nil {
		return nil, err
	}

	pads, err := queueIn.GetSinkPads()
	if err != nil {
		return nil, err
	}
	if len(pads) == 0 {
		return nil, psrpc.NewErrorf(psrpc.Internal, "no sink pad on queue")
	}
	pad := pads[0]
	id := pad.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		return gst.PadProbeDrop
	})
	e.stopDropping = func() {
		pad.RemoveProbe(id)
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

	queueEnc, err := gst.NewElementWithName("queue", fmt.Sprintf("video_%s_enc", layer.Quality.String()))
	if err != nil {
		return nil, err
	}
	if err = queueEnc.SetProperty("max-size-buffers", uint(1)); err != nil {
		return nil, err
	}

	e.elements = []*gst.Element{
		queueIn, videoScale, inputCaps, queueEnc,
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
		// 1s VBV buffer size
		if err = e.enc.SetProperty("vbv-buf-capacity", uint(1000)); err != nil {
			return nil, err
		}
		if err = e.enc.SetProperty("byte-stream", true); err != nil {
			return nil, err
		}
		if err = e.enc.SetProperty("threads", threadCount); err != nil {
			return nil, err
		}

		e.enc.SetArg("tune", "zerolatency")
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
		if err = e.enc.SetProperty("threads", int(threadCount)); err != nil {
			return nil, err
		}
		if err = e.enc.SetProperty("cpu-used", -6); err != nil {
			return nil, err
		}
		if err = e.enc.SetProperty("deadline", int64(1)); err != nil {
			return nil, err
		}
		if err = e.enc.SetProperty("buffer-initial-size", 500); err != nil {
			return nil, err
		}
		if err = e.enc.SetProperty("buffer-optimal-size", 600); err != nil {
			return nil, err
		}
		if err = e.enc.SetProperty("buffer-size", 1000); err != nil {
			return nil, err
		}
		e.enc.SetArg("end-usage", "cbr")

		e.elements = append(e.elements, e.enc)

	default:
		return nil, errors.ErrUnsupportedEncodeFormat
	}

	queueOut, err := gst.NewElementWithName("queue", fmt.Sprintf("video_%s_out", layer.Quality.String()))
	if err != nil {
		return nil, err
	}
	if err = queueOut.SetProperty("max-size-buffers", uint(2)); err != nil {
		return nil, err
	}

	e.elements = append(e.elements, queueOut, e.sink.Element)

	e.bin = gst.NewBin(fmt.Sprintf("video_%s", layer.Quality.String()))
	if err = e.linkElements(); err != nil {
		return nil, err
	}

	return e, nil
}

func NewAudioOutput(options *livekit.IngressAudioEncodingOptions, outputSync *utils.TrackOutputSynchronizer, isPlayingTooSlow func() bool, statsGatherer *stats.LocalMediaStatsGatherer, eos *eosDispatcher) (*AudioOutput, error) {
	e, err := newAudioOutput(options.AudioCodec, outputSync, isPlayingTooSlow, eos)
	if err != nil {
		return nil, err
	}

	e.logger = logger.GetLogger().WithValues("kind", "audio")

	e.trackStatsGatherer = statsGatherer.RegisterTrackStats(stats.OutputAudio)

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

	queueEnc, err := gst.NewElementWithName("queue", "audio_enc")
	if err != nil {
		return nil, err
	}
	if err = queueEnc.SetProperty("max-size-buffers", uint(1)); err != nil {
		return nil, err
	}

	pads, err := queueEnc.GetSinkPads()
	if err != nil {
		return nil, err
	}
	if len(pads) == 0 {
		return nil, psrpc.NewErrorf(psrpc.Internal, "no sink pad on queue")
	}
	pad := pads[0]
	id := pad.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		return gst.PadProbeDrop
	})
	e.stopDropping = func() {
		pad.RemoveProbe(id)
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

	queueOut, err := gst.NewElementWithName("queue", "audio_out")
	if err != nil {
		return nil, err
	}
	if err = queueOut.SetProperty("max-size-time", uint64(5e7)); err != nil {
		return nil, err
	}

	e.elements = []*gst.Element{
		audioConvert, audioResample, capsFilter, queueEnc, e.enc, queueOut, e.sink.Element,
	}

	e.bin = gst.NewBin("audio")
	if err = e.linkElements(); err != nil {
		return nil, err
	}

	e.latencyCaps = gst.NewCapsFromString(packetLatencyCaps)

	return e, nil
}

func newVideoOutput(codec livekit.VideoCodec, outputSync *utils.TrackOutputSynchronizer, isPlayingTooSlow func() bool, eos *eosDispatcher) (*VideoOutput, error) {
	e, err := newOutput(outputSync, isPlayingTooSlow, eos)
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

func newAudioOutput(codec livekit.AudioCodec, outputSync *utils.TrackOutputSynchronizer, isPlayingTooSlow func() bool, eos *eosDispatcher) (*AudioOutput, error) {
	e, err := newOutput(outputSync, isPlayingTooSlow, eos)
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

func newOutput(outputSync *utils.TrackOutputSynchronizer, isPlayingTooSlow func() bool, eos *eosDispatcher) (*Output, error) {
	sink, err := app.NewAppSink()
	if err != nil {
		return nil, err
	}

	e := &Output{
		queue:            utils.NewBlockingQueue[*sample](queueCapacity),
		sink:             sink,
		outputSync:       outputSync,
		isPlayingTooSlow: isPlayingTooSlow,
		eos:              eos,
	}

	if e.eos != nil {
		e.eos.AddListener(e.onSourceEOS)
	}

	e.start()

	return e, nil
}

func (o *Output) SinkReady(localTrack *lksdk_output.LocalTrack) {
	o.localTrack.Store(localTrack)

	if o.stopDropping != nil {
		o.stopDropping()
	}
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
	e.trackStatsGatherer.PLI()

	keyFrame := gst.NewStructure("GstForceKeyUnit")
	if err := keyFrame.SetValue("all-headers", true); err != nil {
		return err
	}
	e.enc.SendEvent(gst.NewCustomEvent(gst.EventTypeCustomDownstream, keyFrame))
	return nil
}

func (e *Output) handleEOS(_ *app.Sink) {
	e.logger.Infow("app sink EOS")

	e.Close()
}

func (e *Output) writeSample(s *sample) error {
	if e.closed.IsBroken() {
		return io.EOF
	}

	// Synchronize the outputs before the network jitter buffer to avoid old samples stuck
	// in the channel from increasing the whole pipeline delay.
	drop, err := e.outputSync.WaitForMediaTime(s.ts, e.isPlayingTooSlow())
	if err != nil {
		return err
	}
	if drop {
		e.logger.Debugw("Dropping sample", "timestamp", s.ts)
		e.trackStatsGatherer.PacketLost(1)
		return nil
	}

	localTrack := e.localTrack.Load()
	if localTrack == nil {
		err = psrpc.NewErrorf(psrpc.Internal, "localTrack unexpectedly nil")
		e.logger.Warnw("unable to write sample", err)
		return err
	}

	// WriteSample seems to return successfully even if the Peer Connection disconnected.
	// We need to return success to the caller even if the PC is disconnected to allow for reconnections
	err = localTrack.WriteSample(*s.s, nil)
	if err != nil {
		return err
	}

	e.trackStatsGatherer.MediaReceived(int64(len(s.s.Data)))

	return nil
}

func (e *Output) start() {
	go func() {
		for {
			s, err := e.queue.PopFront()
			if err != nil {
				// Closing
				return
			}
			err = e.writeSample(s)
			if err != nil && !e.closed.IsBroken() {
				// Store the first write error
				e.pipelineErr.CompareAndSwap(nil, &err)
			}
		}
	}()
}

func (e *Output) QueueLength() int {
	return e.queue.QueueLength()
}

func (e *Output) Close() error {
	e.logger.Debugw("closing output")

	e.closed.Break()
	e.outputSync.Close()
	e.queue.Close()

	return nil
}

func (e *Output) onSourceEOS() {
	e.logger.Debugw("eos received, eventually closing queue after timeout")
	go func() {
		timer := time.NewTimer(eosQueueDrainTimeout)
		defer timer.Stop()

		select {
		case <-timer.C:
			e.Close()
			e.logger.Debugw("output closed on eos timeout")
		case <-e.closed.Watch():
			// already closed as a result of handling EOS in-band
		}
	}()
}

func (e *VideoOutput) handleSample(sink *app.Sink) gst.FlowReturn {
	// Return an error if the last write failed
	if errPtr := e.pipelineErr.Load(); errPtr != nil {
		return errors.ErrorToGstFlowReturn(*errPtr)
	}

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

	segment := s.GetSegment()
	if segment == nil {
		return gst.FlowError
	}

	duration := buffer.Duration()
	pts := buffer.PresentationTimestamp()

	ts := time.Duration(segment.ToRunningTime(gst.FormatTime, uint64(pts)))

	sample := &sample{
		s: &media.Sample{
			Data:     buffer.Bytes(),
			Duration: time.Duration(duration),
		},
		ts: ts,
	}

	e.queue.PushBack(sample)

	e.observeLatency(buffer)

	return gst.FlowOK
}

func (e *AudioOutput) handleSample(sink *app.Sink) gst.FlowReturn {
	// Return an error if the last write failed
	if errPtr := e.pipelineErr.Load(); errPtr != nil {
		return errors.ErrorToGstFlowReturn(*errPtr)
	}

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

	segment := s.GetSegment()
	if buffer == nil {
		return gst.FlowError
	}

	duration := buffer.Duration()
	pts := buffer.PresentationTimestamp()

	ts := time.Duration(segment.ToRunningTime(gst.FormatTime, uint64(pts)))

	switch e.codec {
	case livekit.AudioCodec_OPUS:
		sample := &sample{
			s: &media.Sample{
				Data:     buffer.Bytes(),
				Duration: time.Duration(duration),
			},
			ts: ts,
		}

		e.queue.PushBack(sample)
	}

	e.observeLatency(buffer)

	return gst.FlowOK
}

func getVideoEncoderThreadCount(layer *livekit.VideoLayer) uint {
	threadCount := (int64(layer.Width)*int64(layer.Height) + int64(pixelsPerEncoderThread-1)) / int64(pixelsPerEncoderThread)

	return uint(threadCount)
}

func (e *Output) observeLatency(buffer *gst.Buffer) {
	if e.trackStatsGatherer == nil {
		return
	}

	meta := buffer.GetReferenceTimestampMeta(e.latencyCaps)
	if meta == nil {
		return
	}

	ingestedAt := meta.Timestamp.AsTimestamp()
	if ingestedAt == nil || ingestedAt.IsZero() {
		return
	}

	now := time.Now()
	if !e.latencySampledAt.IsZero() {
		if since := now.Sub(e.latencySampledAt); since < latencySampleInterval {
			return
		}
	}

	latency := now.Sub(*ingestedAt)
	e.trackStatsGatherer.ObserveLatency(latency)
	e.latencySampledAt = now
}
