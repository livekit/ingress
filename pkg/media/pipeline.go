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

	onStatusUpdate func(context.Context, *livekit.IngressInfo)
	closed         chan struct{}
}

func New(ctx context.Context, conf *config.Config, p *Params) (*Pipeline, error) {
	ctx, span := tracer.Start(ctx, "Pipeline.New")
	defer span.End()

	// initialize gst
	gst.Init(nil)

	input, err := NewInput(p)
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

	sink, err := NewWebRTCSink(ctx, conf, p)
	if err != nil {
		return nil, err
	}

	input.OnOutputReady(sink.AddTrack)

	return &Pipeline{
		pipeline: pipeline,
	}, nil
}

func (p *Pipeline) GetInfo() *livekit.IngressInfo {
	return nil
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
		p.InputStatus = &livekit.InputStatus{StatusDescription: err.Error()}
		p.State = livekit.IngressInfo_ENDPOINT_ERROR
		return p.IngressInfo
	}

	// run main loop
	p.loop.Run()

	return nil
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
