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
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-glib/pkg/glib/v2"
	"github.com/go-gst/go-glib/pkg/gobject/v2"
	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/pion/webrtc/v4"

	"go.opentelemetry.io/otel"

	"github.com/livekit/ingress/pkg/gstutil"
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

	// How much of the advertised duration playback may miss and still count as
	// complete, as a percentage of it. A manifest duration is the sum of its
	// segment durations, and the final segment can be shorter than it declares.
	durationTolerancePercent = 5.0

	// Floor under that percentage. On a short asset the percentage collapses to
	// a few tens of milliseconds, which is narrower than the overshoot of a
	// final segment. Unmeasured, like the percentage above it.
	durationToleranceFloor = time.Second
)

type Pipeline struct {
	*params.Params

	// gstreamer
	pipeline gst.Pipeline
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
	gst.Init()

	input, err := NewInput(ctx, params, g)
	if err != nil {
		return nil, err
	}

	pipeline := gstutil.NewPipeline("pipeline")
	if err = gstutil.Add(pipeline, input.bin); err != nil {
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

func (p *Pipeline) onOutputReady(pad gst.Pad, kind types.StreamKind) {
	currentCaps := pad.GetCurrentCaps()
	logger.Debugw("output ready", "kind", kind, "capsAlreadySet", currentCaps != nil)

	pad.NotifyProperty("caps", func(obj gobject.Object, _ *gobject.ParamSpec) {
		p.onParamsReady(kind, obj.(gst.Pad))
	})
}

func (p *Pipeline) onParamsReady(kind types.StreamKind, gPad gst.Pad) {
	var err error

	newCaps := gPad.GetCurrentCaps()
	if newCaps == nil {
		return
	}

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

	if err = gstutil.Add(p.pipeline, bin); err != nil {
		logger.Errorw("could not add bin", err)
		return
	}

	p.trackLock.Lock()
	p.established[kind] = newCaps.String()
	p.trackLock.Unlock()

	gPad.AddProbe(gst.PadProbeTypeBlockDownstream, func(pad gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		// link
		if linkReturn := pad.Link(bin.GetStaticPad("sink")); linkReturn != gst.PadLinkOK {
			logger.Errorw("failed to link output bin", err)
		}

		// sync state
		bin.SyncStateWithParent()

		return gst.PadProbeRemove
	})
}

// fail stops the pipeline and reports err as the session error.
//
// A session that cannot build one of its outputs will never publish that track,
// and a terminal status is read downstream as the session having ended, so it
// must not carry on running under one.
//
// The update goes out last, on a GStreamer streaming thread, and has no
// deadline. Everything that ends the session has already run by then, so a
// stalled update parks this thread alone rather than holding up the teardown.
func (p *Pipeline) fail(err error) {
	// Run reads this once the loop stops, so the session ends with this as its
	// cause rather than as a clean shutdown.
	select {
	case p.pipelineErr <- err:
	default:
	}

	p.SetStatus(livekit.IngressState_ENDPOINT_ERROR, err)

	// SendEOS runs the full shutdown: it cancels the input and stops the loop.
	p.SendEOS(context.Background())

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
	p.pipeline.GetBus().AddWatchFull(glib.PRIORITY_DEFAULT, func(_ gst.Bus, msg *gst.Message) bool {
		return p.messageWatch(msg)
	})

	// set state to playing (this does not start the pipeline)
	if ret := p.pipeline.SetState(gst.StatePlaying); ret == gst.StateChangeFailure {
		err = psrpc.NewErrorf(psrpc.Internal, "could not set pipeline to playing")
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

	return resolveRunError(err, p.pipelineErr)
}

// resolveRunError picks what Run reports from the error the pipeline shut down
// with and any cause recorded on the way down.
//
// A cancellation is how the shutdown unblocks a source that is waiting on
// bytes, so it describes the teardown rather than what caused it. A recorded
// cause is preferred over one. Anything else stands: a genuine input failure is
// more upstream than anything recorded downstream of it.
func resolveRunError(err error, recorded <-chan error) error {
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}

	select {
	case cause := <-recorded:
		return cause
	default:
		return err
	}
}

func (p *Pipeline) messageWatch(msg *gst.Message) bool {
	switch msg.Type() {
	case gst.MessageEOS:
		// EOS received - close and return
		logger.Debugw("EOS received, stopping pipeline")

		// This has to run before the pipeline goes to NULL, since a NULL
		// pipeline answers no position query. A stop we asked for also ends
		// playback early, so it says nothing about the source: skip the check
		// then.
		if !p.closed.IsBroken() {
			if err := p.checkSourceComplete(); err != nil {
				select {
				case p.pipelineErr <- err:
				default:
				}
			}
		}

		_ = gstutil.SetStateBlocking(p.pipeline, gst.StateNull)
		p.loop.Quit()
		return false

	case gst.MessageError:
		// handle error if possible, otherwise close and return
		_, gerr := msg.ParseError()
		err := psrpc.NewError(psrpc.Internal, gerr)
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
		if gstutil.SourceName(msg) == p.pipeline.GetName() {
			logger.Infow("GST ASYNC_START (pipeline)")
		}

	case gst.MessageAsyncDone:
		if gstutil.SourceName(msg) == p.pipeline.GetName() {
			logger.Debugw("GST ASYNC_DONE (pipeline)")
		}

	case gst.MessageTag, gst.MessageLatency, gst.MessageStreamStatus, gst.MessageElement:
		// ignore

	default:
		logger.Debugw(msg.String())
	}

	return true
}

// checkSourceComplete returns an error when EOS arrived before the source
// played out the duration it advertises. A segment fetch that fails for good
// arrives as an ordinary EOS, so the advertised duration is what separates a
// truncated pull from a finished one.
//
// Only a source being pulled over HTTP has a duration to fall short of. Live
// inputs are excluded by what they are rather than by the duration query
// declining for them, because that query is answered from the timestamps that
// have arrived: the gap it reports is pipeline latency at teardown, not
// missing source, and it widens as a share of the whole the shorter the
// session is. RTMP was seen answering it.
//
// That rules out the push inputs, and the srt:// and udp:// urls a URL pull
// also accepts. The prefix test is the one NewURLSource selects the source
// element with, so the two cannot disagree about what HTTP is. Naming HTTP
// rather than the live schemes is deliberate: a scheme added later stays
// outside the check until someone has decided it belongs there.
//
// Within HTTP pulls this catches HLS. A playlist declares no end, so the
// position query is answered upstream, by what actually arrived. mp4 and
// matroska do declare an end, so their sinks answer it instead and this
// returns nil.
func (p *Pipeline) checkSourceComplete() error {
	httpPull := p.InputType == livekit.IngressInput_URL_INPUT &&
		(strings.HasPrefix(p.Url, "http://") || strings.HasPrefix(p.Url, "https://"))
	if !httpPull {
		return nil
	}

	d, ok := p.pipeline.QueryDuration(gst.FormatTime)
	if !ok || d <= 0 {
		return nil
	}

	pos, ok := p.pipeline.QueryPosition(gst.FormatTime)
	if !ok || pos <= 0 {
		logger.Debugw("no position at EOS, treating the source as complete",
			"duration", time.Duration(d))
		return nil
	}

	duration, position := time.Duration(d), time.Duration(pos)
	tolerance := max(
		time.Duration(float64(duration)*durationTolerancePercent/100),
		durationToleranceFloor,
	)
	if position+tolerance >= duration {
		return nil
	}

	logger.Warnw("source ended before the end of the stream", nil,
		"position", position, "duration", duration)

	return psrpc.NewErrorf(psrpc.Unavailable,
		"source ended after %s of %s", position, duration)
}

func (p *Pipeline) handleStreamCollectionMessage(msg *gst.Message) {
	collection := msg.ParseStreamCollection()

	for i := uint32(0); i < collection.GetSize(); i++ {
		stream := collection.GetStream(i)

		caps := stream.GetCaps()
		if caps == nil || caps.GetSize() == 0 {
			continue
		}

		gstStruct := caps.GetStructure(0)

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
	old, new, _ := msg.ParseStateChanged()
	isPipeline := gstutil.SourceName(msg) == p.pipeline.GetName()

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
		err := gstutil.SetStateBlocking(p.pipeline, gst.StateNull)
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
	glib.IdleAddFull(glib.PRIORITY_DEFAULT_IDLE, func() bool {
		p.loop.Quit()
		return false
	})
}

func (p *Pipeline) GetGstPipelineDebugDot() string {
	return p.pipeline.DebugBinToDotData(gst.DebugGraphShowAll)
}

func getKindFromGstMimeType(gstStruct *gst.Structure) types.StreamKind {
	gstMimeType := gstStruct.GetName()

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
	gstMimeType := gstStruct.GetName()

	switch strings.ToLower(gstMimeType) {
	case "audio/mpeg":
		mime = gstMimeType
		version, _ := gstStruct.GetInt("mpegversion")

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

	if channels, ok := gstStruct.GetInt("channels"); ok {
		audioState.Channels = uint32(channels)
	}

	if rate, ok := gstStruct.GetInt("rate"); ok {
		audioState.SampleRate = uint32(rate)
	}

	return audioState
}

func getVideoState(gstStruct *gst.Structure) *livekit.InputVideoState {
	mime := ""

	gstMimeType := gstStruct.GetName()

	switch strings.ToLower(gstMimeType) {
	case "video/x-h264":
		mime = webrtc.MimeTypeH264
	default:
		mime = gstMimeType
	}

	videoState := &livekit.InputVideoState{
		MimeType: mime,
	}

	if width, ok := gstStruct.GetInt("width"); ok {
		videoState.Width = uint32(width)
	}

	if height, ok := gstStruct.GetInt("height"); ok {
		videoState.Height = uint32(height)
	}

	if num, denom, ok := gstStruct.GetFraction("framerate"); ok && denom != 0 {
		videoState.Framerate = float64(num) / float64(denom)
	}

	return videoState
}
