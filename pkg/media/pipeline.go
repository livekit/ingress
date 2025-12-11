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
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/pion/webrtc/v4"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	"github.com/livekit/psrpc"
)

const (
	creationTimeout = 10 * time.Second
)

type Pipeline struct {
	*params.Params

	// gstreamer
	pipeline *gst.Pipeline
	loop     *glib.MainLoop
	sink     *WebRTCSink
	input    *Input

	closed core.Fuse
	cancel atomic.Pointer[context.CancelFunc]

	pipelineErr chan error

	eos *eosDispatcher
}

func New(ctx context.Context, conf *config.Config, params *params.Params, g *stats.LocalMediaStatsGatherer) (*Pipeline, error) {
	ctx, span := tracer.Start(ctx, "Pipeline.New")
	defer span.End()

	ctx, done := context.WithTimeout(ctx, creationTimeout)
	defer done()

	// initialize gst
	gst.Init(nil)

	input, err := NewInput(ctx, params, g)
	if err != nil {
		return nil, err
	}

	pipeline, err := gst.NewPipeline("pipeline")
	if err != nil {
		return nil, err
	}

	if err = pipeline.Add(input.bin.Element); err != nil {
		return nil, err
	}

	p := &Pipeline{
		Params:      params,
		pipeline:    pipeline,
		input:       input,
		pipelineErr: make(chan error, 1),
		eos:         newEOSDispatcher(),
	}

	input.SetOnEOS(p.eos.Fire)

	sink, err := NewWebRTCSink(ctx, params, func() {
		if cancel := p.cancel.Load(); cancel != nil {
			(*cancel)()
		}

		if p.loop != nil {
			p.loop.Quit()
		}
	}, g, p.eos)
	if err != nil {
		return nil, err
	}
	p.sink = sink

	input.OnOutputReady(p.onOutputReady)

	return p, nil
}

func (p *Pipeline) onOutputReady(pad *gst.Pad, kind types.StreamKind) {
	var err error
	defer func() {
		if err != nil {
			p.SetStatus(livekit.IngressState_ENDPOINT_ERROR, err)
			p.SendStateUpdate(context.Background())
		}
	}()

	_, err = pad.Connect("notify::caps", func(gPad *gst.GhostPad, param *glib.ParamSpec) {
		p.onParamsReady(kind, gPad, param)
	})
}

func (p *Pipeline) onParamsReady(kind types.StreamKind, gPad *gst.GhostPad, param *glib.ParamSpec) {
	var err error

	// TODO fix go-gst to not create non nil gst.Caps for a NULL native caps pointer?
	caps, err := gPad.Pad.GetProperty("caps")
	if err != nil || caps == nil || caps.(*gst.Caps) == nil || caps.(*gst.Caps).Unsafe() == nil {
		return
	}

	defer func() {
		if err != nil {
			p.SetStatus(livekit.IngressState_ENDPOINT_ERROR, err)
		} else {
			p.SetStatus(livekit.IngressState_ENDPOINT_PUBLISHING, nil)
		}

		// Is it ok to send this message here? The update handler is not waiting for a response but still doing I/O.
		// We could send this in a separate goroutine, but this would make races more likely.
		p.SendStateUpdate(context.Background())
	}()

	bin, err := p.sink.AddTrack(kind, caps.(*gst.Caps))
	if err != nil {
		return
	}

	if err = p.pipeline.Add(bin.Element); err != nil {
		logger.Errorw("could not add bin", err)
		return
	}

	gPad.AddProbe(gst.PadProbeTypeBlockDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		// link
		if linkReturn := pad.Link(bin.GetStaticPad("sink")); linkReturn != gst.PadLinkOK {
			logger.Errorw("failed to link output bin", err)
		}

		// sync state
		bin.SyncStateWithParent()

		return gst.PadProbeRemove
	})
}

func (p *Pipeline) Run(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "Pipeline.Run")
	defer span.End()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	p.cancel.Store(&cancel)

	var err error

	// add watch
	p.loop = glib.NewMainLoop(glib.MainContextDefault(), false)
	p.pipeline.GetPipelineBus().AddWatch(p.messageWatch)

	// set state to playing (this does not start the pipeline)
	err = p.pipeline.Start()
	if err != nil {
		span.RecordError(err)
		logger.Errorw("failed to set pipeline state", err)
		return err
	}

	err = p.input.Start(ctx, func(ctx context.Context) {
		p.SendEOS(ctx)
	})
	if err != nil {
		span.RecordError(err)
		logger.Infow("failed to start input", err)
		p.SetStatus(livekit.IngressState_ENDPOINT_ERROR, err)
		return err
	}

	logger.Infow("starting GST pipeline")

	// run main loop
	p.loop.Run()

	logger.Infow("GST pipeline stopped")

	// Return the error from the most upstream part of the pipeline
	err = p.input.Close()
	sinkErr := p.sink.Close()

	if err == nil || (err == context.Canceled && sinkErr != nil) {
		// prefer sink error if exists (causal and more specific) over the generic context.Canceled error
		err = sinkErr
	}

	if err == nil {
		// Retrieve any pipeline error
		select {
		case err = <-p.pipelineErr:
		default:
		}
	}

	return err
}

func (p *Pipeline) messageWatch(msg *gst.Message) bool {
	switch msg.Type() {
	case gst.MessageEOS:
		// EOS received - close and return
		logger.Debugw("EOS received, stopping pipeline")
		_ = p.pipeline.BlockSetState(gst.StateNull)
		p.loop.Quit()
		return false

	case gst.MessageError:
		// handle error if possible, otherwise close and return
		err := psrpc.NewError(psrpc.Internal, msg.ParseError())
		logger.Infow("pipeline failure", "error", msg)
		select {
		case p.pipelineErr <- err:
		default:
		}
		p.loop.Quit()
		return false

	case gst.MessageStreamCollection:
		p.handleStreamCollectionMessage(msg)

	case gst.MessageStateChanged:
		p.logPipelineStateChange(msg)

	case gst.MessageAsyncStart:
		src := msg.Source()
		if src == p.pipeline.GetName() {
			logger.Infow("GST ASYNC_START (pipeline)")
		}

	case gst.MessageAsyncDone:
		src := msg.Source()
		if src == p.pipeline.GetName() {
			logger.Debugw("GST ASYNC_DONE (pipeline)")
		}

	case gst.MessageTag, gst.MessageLatency, gst.MessageStreamStatus, gst.MessageElement:
		// ignore

	default:
		logger.Debugw(msg.String())
	}

	return true
}

func (p *Pipeline) handleStreamCollectionMessage(msg *gst.Message) {
	collection := msg.ParseStreamCollection()
	if collection == nil {
		return
	}

	for i := uint(0); i < collection.GetSize(); i++ {
		stream := collection.GetStreamAt(i)

		caps := stream.Caps()
		if caps == nil || caps.GetSize() == 0 {
			continue
		}

		gstStruct := stream.Caps().GetStructureAt(0)

		kind := getKindFromGstMimeType(gstStruct)
		switch kind {
		case types.Audio:
			audioState := getAudioState(gstStruct)
			p.SetInputAudioState(context.Background(), audioState, true)
		case types.Video:
			videoState := getVideoState(gstStruct)
			p.SetInputVideoState(context.Background(), videoState, true)
		}
	}
}

func (p *Pipeline) logPipelineStateChange(msg *gst.Message) {
	old, new := msg.ParseStateChanged()
	src := msg.Source()
	isPipeline := (src == p.pipeline.GetName())

	if isPipeline && new != old {
		logger.Infow("GST pipeline state changed",
			"old", old, "new", new)
	}
}

func (p *Pipeline) SendEOS(ctx context.Context) {
	ctx, span := tracer.Start(ctx, "Pipeline.SendEOS")
	defer span.End()

	p.closed.Once(func() {
		logger.Debugw("closing pipeline")

		if cancel := p.cancel.Load(); cancel != nil {
			(*cancel)()
		}

		c := make(chan struct{})

		go func() {
			err := p.pipeline.BlockSetState(gst.StateNull)
			if err != nil {
				logger.Errorw("failed stopping pipeline", err)
			}

			close(c)
		}()

		go func() {
			t := time.NewTimer(5 * time.Second)

			select {
			case <-c:
				t.Stop()
			case <-t.C:
				// Do not set ingress in error state as we are stopping and this causes some media at the end
				// to not be sent to the room at worse
				logger.Errorw("pipeline frozen", psrpc.NewErrorf(psrpc.Internal, "pipeline frozen"))
			}

			p.loop.Quit()
		}()
	})
}

func (p *Pipeline) GetGstPipelineDebugDot() string {
	return p.pipeline.DebugBinToDotData(gst.DebugGraphShowAll)
}

func getKindFromGstMimeType(gstStruct *gst.Structure) types.StreamKind {
	gstMimeType := gstStruct.Name()

	switch {
	case strings.HasPrefix(gstMimeType, "audio"):
		return types.Audio
	case strings.HasPrefix(gstMimeType, "video"):
		return types.Video
	default:
		return types.Unknown
	}
}

func getAudioState(gstStruct *gst.Structure) *livekit.InputAudioState {
	mime := ""
	gstMimeType := gstStruct.Name()

	switch strings.ToLower(gstMimeType) {
	case "audio/mpeg":
		mime = gstMimeType
		var version int

		val, err := gstStruct.GetValue("mpegversion")
		if err == nil {
			version, _ = val.(int)
		}

		if version == 4 {
			mime = "audio/aac"
		}
	case "audio/x-opus":
		mime = webrtc.MimeTypeOpus
	default:
		mime = gstMimeType
	}

	audioState := &livekit.InputAudioState{
		MimeType: mime,
	}

	val, err := gstStruct.GetValue("channels")
	if err == nil {
		channels, _ := val.(int)
		audioState.Channels = uint32(channels)
	}

	val, err = gstStruct.GetValue("rate")
	if err == nil {
		rate, _ := val.(int)
		audioState.SampleRate = uint32(rate)
	}

	return audioState
}

func getVideoState(gstStruct *gst.Structure) *livekit.InputVideoState {
	mime := ""

	gstMimeType := gstStruct.Name()

	switch strings.ToLower(gstMimeType) {
	case "video/x-h264":
		mime = webrtc.MimeTypeH264
	default:
		mime = gstMimeType
	}

	videoState := &livekit.InputVideoState{
		MimeType: mime,
	}

	val, err := gstStruct.GetValue("width")
	if err == nil {
		width, _ := val.(int)
		videoState.Width = uint32(width)
	}

	val, err = gstStruct.GetValue("height")
	if err == nil {
		height, _ := val.(int)
		videoState.Height = uint32(height)
	}

	val, err = gstStruct.GetValue("framerate")
	if err == nil {
		fpsFrac, _ := val.(*gst.FractionValue)

		if fpsFrac.Denom() != 0 {
			videoState.Framerate = float64(fpsFrac.Num()) / float64(fpsFrac.Denom())
		}
	}

	return videoState
}
