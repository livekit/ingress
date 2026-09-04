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
	"sync"
	"sync/atomic"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/pion/webrtc/v4"

	"go.opentelemetry.io/otel"

	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/psrpc"
)

var tracer = otel.Tracer("github.com/livekit/ingress/pkg/media")

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

	trackLock sync.Mutex

	// The caps each stream kind's output was built from.
	established map[types.StreamKind]string
}

func New(ctx context.Context, params *params.Params, g *stats.LocalMediaStatsGatherer) (*Pipeline, error) {
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
		Params:   params,
		pipeline: pipeline,
		input:    input,
		// SendEOS can reach the loop before Run does, so it is built here
		// rather than on the way into the loop, and is never nil.
		loop:        glib.NewMainLoop(glib.MainContextDefault(), false),
		pipelineErr: make(chan error, 1),
		eos:         newEOSDispatcher(),
		established: make(map[types.StreamKind]string),
	}

	input.SetOnEOS(p.eos.Fire)

	sink, err := NewWebRTCSink(ctx, params, func() {
		if cancel := p.cancel.Load(); cancel != nil {
			(*cancel)()
		}

		p.quitLoop()
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
			p.fail(err)
		}
	}()

	currentCaps := pad.GetCurrentCaps()
	logger.Debugw("output ready", "kind", kind, "capsAlreadySet", currentCaps != nil)

	_, err = pad.Connect("notify::caps", func(gPad *gst.GhostPad, _ *glib.ParamSpec) {
		p.onParamsReady(kind, gPad)
	})
}

func (p *Pipeline) onParamsReady(kind types.StreamKind, gPad *gst.GhostPad) {
	var err error

	// TODO fix go-gst to not create non nil gst.Caps for a NULL native caps pointer?
	caps, err := gPad.GetProperty("caps")
	if err != nil || caps == nil || caps.(*gst.Caps) == nil || caps.(*gst.Caps).Unsafe() == nil {
		return
	}

	newCaps := caps.(*gst.Caps)

	// The audio and video pads notify on separate GStreamer streaming threads,
	// so this map is shared mutable state and every access takes the lock.
	p.trackLock.Lock()
	builtCaps, built := p.established[kind]
	p.trackLock.Unlock()

	if built {
		// Rebuilding is not an option -- it adds a second output of the same name
		// and cannot restructure a published track -- so the session continues on
		// the output it has. Caps the pipeline genuinely cannot take fail
		// negotiation downstream and surface on the bus.
		logger.Warnw("caps renegotiated after the output was built, continuing on the existing output", nil,
			"kind", kind, "establishedCaps", builtCaps, "newCaps", newCaps.String())
		return
	}

	defer func() {
		if err != nil {
			p.fail(err)
			return
		}

		p.SetStatus(livekit.IngressState_ENDPOINT_PUBLISHING, nil)

		// Is it ok to send this message here? The update handler is not waiting for a response but still doing I/O.
		// We could send this in a separate goroutine, but this would make races more likely.
		p.SendStateUpdate(context.Background())
	}()

	bin, err := p.sink.AddTrack(kind, newCaps)
	if err != nil {
		return
	}

	if err = p.pipeline.Add(bin.Element); err != nil {
		logger.Errorw("could not add bin", err)
		return
	}

	p.trackLock.Lock()
	p.established[kind] = newCaps.String()
	p.trackLock.Unlock()

	gPad.AddProbe(gst.PadProbeTypeBlockDownstream, func(pad *gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		// link
		if linkReturn := pad.Link(bin.GetStaticPad("sink")); linkReturn != gst.PadLinkOK {
			logger.Errorw("failed to link output bin", err)
		}

		// sync state
		bin.SyncStateWithParent()

		return gst.PadProbeRemove
	})
}

// fail stops the pipeline and reports err.
//
// A session that cannot build one of its outputs will never publish that track,
// and a terminal status is read downstream as the session having ended, so it
// must not carry on running under one.
func (p *Pipeline) fail(err error) {
	// Run drains this once the loop stops, so the session ends with this as its
	// cause rather than as a clean shutdown.
	select {
	case p.pipelineErr <- err:
	default:
	}

	// Stop before reporting: the update below is a blocking round trip with no
	// deadline, and the shutdown must not wait on it.
	p.quitLoop()

	p.SetStatus(livekit.IngressState_ENDPOINT_ERROR, err)
	p.SendStateUpdate(context.Background())
}

func (p *Pipeline) Run(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "Pipeline.Run")
	defer span.End()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	p.cancel.Store(&cancel)

	// Shutdown already requested: bail out before starting anything. Only the
	// sink needs closing; New brought it up, while the input has not started
	// and closing an unstarted one blocks.
	if p.closed.IsBroken() {
		logger.Infow("shutdown requested before the pipeline started")
		return p.sink.Close()
	}

	var err error

	// add watch
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
			p.SetInputAudioState(context.Background(), audioState, true, false)
		case types.Video:
			videoState := getVideoState(gstStruct)
			p.SetInputVideoState(context.Background(), videoState, true, false)
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
	_, span := tracer.Start(ctx, "Pipeline.SendEOS")
	defer span.End()

	// Break before loading the cancel below. Run stores its cancel and then
	// checks the fuse, so this order leaves no interleaving where Run both
	// misses the check and has not yet stored a cancel for this to find.
	if !p.closed.Break() {
		return
	}

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

		p.quitLoop()
	}()
}

// quitLoop stops the main loop, and is safe to call before Run has started it:
// the quit is queued on the main context and dispatched when the loop runs.
// Calling Quit directly is not safe there, because g_main_loop_run sets
// is_running=TRUE on entry and overwrites it. Assumes this is the only main
// loop on the default context.
func (p *Pipeline) quitLoop() {
	if _, err := glib.IdleAdd(p.loop.Quit); err != nil {
		logger.Errorw("failed to schedule loop quit, quitting directly", err)
		p.loop.Quit()
	}
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
