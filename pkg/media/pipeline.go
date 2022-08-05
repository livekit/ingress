package media

import (
	"context"

	"github.com/tinyzimmer/go-glib/glib"
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/tracer"
)

type Pipeline struct {
	*Params

	// gstreamer
	pipeline *gst.Pipeline
	loop     *glib.MainLoop
	sink     *WebRTCSink
	input    *Input

	onStatusUpdate func(context.Context, *livekit.IngressInfo)
	closed         chan struct{}
}

func New(ctx context.Context, conf *config.Config, params *Params) (*Pipeline, error) {
	ctx, span := tracer.Start(ctx, "Pipeline.New")
	defer span.End()

	// initialize gst
	gst.Init(nil)

	params.Logger.Debugw("listening", "url", params.Url)

	input, err := NewInput(params)
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
		closed:   make(chan struct{}),
	}

	input.OnOutputReady(p.onOutputReady)

	return p, nil
}

func (p *Pipeline) onOutputReady(pad *gst.Pad, kind StreamKind) {
	var bin *gst.Bin
	var err error

	defer func() {
		if err != nil {
			p.IngressInfo.State = &livekit.IngressState{
				Status: livekit.IngressState_ENDPOINT_ERROR,
				Error:  err.Error(),
			}
		} else {
			p.IngressInfo.State = &livekit.IngressState{
				Status: livekit.IngressState_ENDPOINT_PUBLISHING,
			}
		}

		if p.onStatusUpdate != nil {
			// Is it ok to send this message here? The update handler is not waiting for a response but still doing I/O.
			// We could send this in a separate goroutine, but this would make races more likely.
			p.onStatusUpdate(context.Background(), p.IngressInfo)
		}
	}()

	bin, err = p.sink.AddTrack(kind)
	if err != nil {
		return
	}

	if err = p.pipeline.Add(bin.Element); err != nil {
		p.Logger.Errorw("could not add bin", err)
		return
	}

	pad.AddProbe(gst.PadProbeTypeBlockDownstream, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		// link
		if linkReturn := pad.Link(bin.GetStaticPad("sink")); linkReturn != gst.PadLinkOK {
			p.Logger.Errorw("failed to link output bin", err)
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
		p.Logger.Errorw("failed to set pipeline state", err)
		p.State = &livekit.IngressState{
			Status: livekit.IngressState_ENDPOINT_ERROR,
			Error:  err.Error(),
		}
		return p.IngressInfo
	}

	err := p.input.Start(ctx)
	if err != nil {
		span.RecordError(err)
		p.Logger.Errorw("failed to start input", err)
		p.State = &livekit.IngressState{
			Status: livekit.IngressState_ENDPOINT_ERROR,
			Error:  err.Error(),
		}
		return p.IngressInfo
	}

	// run main loop
	p.loop.Run()

	err = p.input.Close()
	p.sink.Close()

	switch err {
	case nil:
		p.IngressInfo.State.Status = livekit.IngressState_ENDPOINT_INACTIVE
	default:
		p.IngressInfo.State.Status = livekit.IngressState_ENDPOINT_ERROR
		p.IngressInfo.State.Error = err.Error()
	}

	return p.IngressInfo
}

func (p *Pipeline) messageWatch(msg *gst.Message) bool {
	switch msg.Type() {
	case gst.MessageEOS:
		// EOS received - close and return
		p.Logger.Debugw("EOS received, stopping pipeline")
		_ = p.pipeline.BlockSetState(gst.StateNull)
		p.loop.Quit()
		return false

	case gst.MessageError:
		// handle error if possible, otherwise close and return
		err := errors.New(msg.ParseError().Error())
		p.Logger.Errorw("pipeline failure", err)
		p.loop.Quit()
		return false

	case gst.MessageTag, gst.MessageStateChanged:
		// ignore

	default:
		p.Logger.Debugw(msg.String())
	}

	return true
}

func (p *Pipeline) SendEOS(ctx context.Context) {
	ctx, span := tracer.Start(ctx, "Pipeline.SendEOS")
	defer span.End()

	select {
	case <-p.closed:
		return
	default:
		close(p.closed)
		if p.onStatusUpdate != nil {
			p.onStatusUpdate(ctx, p.IngressInfo)
		}

		p.Logger.Debugw("sending EOS to pipeline")
		p.pipeline.SendEvent(gst.NewEOSEvent())
	}
}
