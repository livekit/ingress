package service

import (
	"context"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/media"
	"github.com/livekit/livekit-server/pkg/service/rpc"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

type Handler struct {
	conf      *config.Config
	pipeline  *media.Pipeline
	rpcClient rpc.IOInfoClient
	kill      chan struct{}
}

func NewHandler(conf *config.Config, rpcClient rpc.IOInfoClient) *Handler {
	return &Handler{
		conf:      conf,
		rpcClient: rpcClient,
		kill:      make(chan struct{}),
	}
}

func (h *Handler) HandleIngress(ctx context.Context, info *livekit.IngressInfo, wsUrl string, token string) {
	ctx, span := tracer.Start(ctx, "Handler.HandleRequest")
	defer span.End()

	p, err := h.buildPipeline(ctx, info, wsUrl, token)
	if err != nil {
		span.RecordError(err)
		return
	}
	h.pipeline = p

	// start ingress
	result := make(chan *livekit.IngressInfo, 1)
	go func() {
		result <- p.Run(ctx)
	}()

	for {
		select {
		case <-h.kill:
			// kill signal received
			p.SendEOS(ctx)

		case res := <-result:
			// recording finished
			h.sendUpdate(ctx, res)
			return
		}
	}
}

func (h *Handler) HangUpIngress(ctx context.Context, req *rpc.HangUpIngressRequest) (*rpc.HangUpIngressResponse, error) {
	_, span := tracer.Start(ctx, "Handler.HangUpIngress")
	defer span.End()

	h.Kill()
	return &rpc.HangUpIngressResponse{}, nil
}

func (h *Handler) buildPipeline(ctx context.Context, info *livekit.IngressInfo, wsUrl string, token string) (*media.Pipeline, error) {
	ctx, span := tracer.Start(ctx, "Handler.buildPipeline")
	defer span.End()

	// build/verify params
	var p *media.Pipeline
	params, err := media.GetParams(ctx, h.conf, info, wsUrl, token)
	if err == nil {
		// create the pipeline
		p, err = media.New(ctx, h.conf, params)
	}

	if err != nil {
		if params != nil {
			info = params.IngressInfo
		}

		info.State.Error = err.Error()
		info.State.Status = livekit.IngressState_ENDPOINT_ERROR
		h.sendUpdate(ctx, info)
		return nil, err
	}

	p.OnStatusUpdate(h.sendUpdate)
	return p, nil
}

func (h *Handler) sendUpdate(ctx context.Context, info *livekit.IngressInfo) {
	switch info.State.Status {
	case livekit.IngressState_ENDPOINT_ERROR:
		logger.Errorw("ingress failed", errors.New(info.State.Error),
			"ingressID", info.IngressId,
		)
	case livekit.IngressState_ENDPOINT_INACTIVE:
		logger.Infow("ingress complete", "ingressID", info.IngressId)
	default:
		logger.Infow("ingress update", "ingressID", info.IngressId)
	}

	_, err := h.rpcClient.UpdateIngressState(ctx, &livekit.UpdateIngressStateRequest{
		IngressId: info.IngressId,
		State:     info.State,
	})
	if err != nil {
		logger.Errorw("failed to send update", err)
	}
}

func (h *Handler) Kill() {
	select {
	case <-h.kill:
		return
	default:
		close(h.kill)
	}
}
