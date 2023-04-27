package media

import (
	"context"
	"time"

	"github.com/frostbyte73/core"
	"github.com/tinyzimmer/go-glib/glib"
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/params"
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

	onStatusUpdate func(context.Context, *livekit.IngressInfo)
	closed         core.Fuse
}

func New(ctx context.Context, conf *config.Config, params *params.Params) (*Pipeline, error) {
	ctx, span := tracer.Start(ctx, "Pipeline.New")
	defer span.End()

	ctx, done := context.WithTimeout(ctx, creationTimeout)
	defer done()

	// initialize gst
	gst.Init(nil)

	logger.Debugw("listening", "url", params.Url)

	input, err := NewInput(ctx, params)
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

	sink, err := NewWebRTCSink(ctx, params)
	if err != nil {
		return nil, err
	}

	p := &Pipeline{
		Params:   params,
		pipeline: pipeline,
		sink:     sink,
		input:    input,
		closed:   core.NewFuse(),
	}

	input.OnOutputReady(p.onOutputReady)

	return p, nil
}

func (p *Pipeline) onOutputReady(pad *gst.Pad, kind types.StreamKind) {
	var err error

	defer func() {
		if err != nil {
			p.SetStatus(livekit.IngressState_ENDPOINT_ERROR, err.Error())
		} else {
			p.SetStatus(livekit.IngressState_ENDPOINT_PUBLISHING, "")
		}

		if p.onStatusUpdate != nil {
			// Is it ok to send this message here? The update handler is not waiting for a response but still doing I/O.
			// We could send this in a separate goroutine, but this would make races more likely.
			p.onStatusUpdate(context.Background(), p.GetInfo())
		}
	}()

	bin, err := p.sink.AddTrack(kind)
	if err != nil {
		return
	}

	if err = p.pipeline.Add(bin.Element); err != nil {
		logger.Errorw("could not add bin", err)
		return
	}

	pad.AddProbe(gst.PadProbeTypeBlockDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		// link
		if linkReturn := pad.Link(bin.GetStaticPad("sink")); linkReturn != gst.PadLinkOK {
			logger.Errorw("failed to link output bin", err)
		}

		// sync state
		bin.SyncStateWithParent()

		return gst.PadProbeRemove
	})
}

func (p *Pipeline) GetInfo() *livekit.IngressInfo {
	return p.Params.IngressInfo
}

func (p *Pipeline) OnStatusUpdate(f func(context.Context, *livekit.IngressInfo)) {
	p.onStatusUpdate = f
}

func (p *Pipeline) Run(ctx context.Context) *livekit.IngressInfo {
	ctx, span := tracer.Start(ctx, "Pipeline.Run")
	defer span.End()

	// add watch
	p.loop = glib.NewMainLoop(glib.MainContextDefault(), false)
	p.pipeline.GetPipelineBus().AddWatch(p.messageWatch)

	// set state to playing (this does not start the pipeline)
	if err := p.pipeline.Start(); err != nil {
		span.RecordError(err)
		logger.Errorw("failed to set pipeline state", err)
		p.SetStatus(livekit.IngressState_ENDPOINT_ERROR, err.Error())
		return p.GetInfo()
	}

	err := p.input.Start(ctx)
	if err != nil {
		span.RecordError(err)
		logger.Errorw("failed to start input", err)
		p.SetStatus(livekit.IngressState_ENDPOINT_ERROR, err.Error())
		return p.GetInfo()
	}

	// run main loop
	p.loop.Run()

	err = p.input.Close()
	p.sink.Close()

	switch err {
	case nil:
		p.SetStatus(livekit.IngressState_ENDPOINT_INACTIVE, "")
	default:
		p.SetStatus(livekit.IngressState_ENDPOINT_ERROR, err.Error())
	}

	return p.GetInfo()
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
		logger.Errorw("pipeline failure", err)
		p.loop.Quit()
		return false

	case gst.MessageTag, gst.MessageStateChanged:
		// ignore

	default:
		logger.Debugw(msg.String())
	}

	return true
}

func (p *Pipeline) SendEOS(ctx context.Context) {
	ctx, span := tracer.Start(ctx, "Pipeline.SendEOS")
	defer span.End()

	p.closed.Once(func() {
		if p.onStatusUpdate != nil {
			p.onStatusUpdate(ctx, p.GetInfo())
		}

		logger.Debugw("sending EOS to pipeline")
		p.pipeline.SendEvent(gst.NewEOSEvent())
	})
}
