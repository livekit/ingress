package service

import (
	"context"
	"net"

	"google.golang.org/grpc"
	google_protobuf2 "google.golang.org/protobuf/types/known/emptypb"

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/ipc"
	"github.com/livekit/ingress/pkg/media"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/pprof"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/tracer"
)

type Handler struct {
	ipc.UnimplementedIngressHandlerServer

	conf       *config.Config
	pipeline   *media.Pipeline
	rpcClient  rpc.IOInfoClient
	grpcServer *grpc.Server
	kill       core.Fuse
	done       core.Fuse
}

func NewHandler(conf *config.Config, rpcClient rpc.IOInfoClient) *Handler {
	return &Handler{
		conf:       conf,
		rpcClient:  rpcClient,
		grpcServer: grpc.NewServer(),
		kill:       core.NewFuse(),
		done:       core.NewFuse(),
	}
}

func (h *Handler) HandleIngress(ctx context.Context, info *livekit.IngressInfo, wsUrl, token string, extraParams any) {
	ctx, span := tracer.Start(ctx, "Handler.HandleRequest")
	defer span.End()

	p, err := h.buildPipeline(ctx, info, wsUrl, token, extraParams)
	if err != nil {
		span.RecordError(err)
		return
	}
	h.pipeline = p

	listener, err := net.Listen(network, getSocketAddress(p.TmpDir))
	if err != nil {
		span.RecordError(err)
		logger.Errorw("failed starting grpc listener", err)
		return
	}

	ipc.RegisterIngressHandlerServer(h.grpcServer, h)

	go func() {
		err := h.grpcServer.Serve(listener)
		if err != nil {
			span.RecordError(err)
			logger.Errorw("failed statrting grpc handler", err)
		}
	}()

	// start ingress
	result := make(chan struct{}, 1)
	go func() {
		p.Run(ctx)
		result <- struct{}{}
		h.done.Break()
	}()

	kill := h.kill.Watch()

	for {
		select {
		case <-kill:
			// kill signal received
			p.SendEOS(ctx)
			kill = nil

		case <-result:
			// ingress finished
			return
		}
	}
}

func (h *Handler) killAndReturnState(ctx context.Context) (*livekit.IngressState, error) {
	h.Kill()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-h.done.Watch():
		return h.pipeline.CopyInfo().State, nil
	}
}

func (h *Handler) UpdateIngress(ctx context.Context, req *livekit.UpdateIngressRequest) (*livekit.IngressState, error) {
	_, span := tracer.Start(ctx, "Handler.UpdateIngress")
	defer span.End()
	return h.killAndReturnState(ctx)
}

func (h *Handler) DeleteIngress(ctx context.Context, req *livekit.DeleteIngressRequest) (*livekit.IngressState, error) {
	_, span := tracer.Start(ctx, "Handler.DeleteIngress")
	defer span.End()
	return h.killAndReturnState(ctx)
}

func (h *Handler) DeleteWHIPResource(ctx context.Context, req *rpc.DeleteWHIPResourceRequest) (*google_protobuf2.Empty, error) {
	_, span := tracer.Start(ctx, "Handler.DeleteWHIPResource")
	defer span.End()

	h.killAndReturnState(ctx)

	return &google_protobuf2.Empty{}, nil
}

func (h *Handler) GetPProf(ctx context.Context, req *ipc.PProfRequest) (*ipc.PProfResponse, error) {
	ctx, span := tracer.Start(ctx, "Handler.GetPProf")
	defer span.End()

	if h.pipeline == nil {
		return nil, errors.ErrIngressNotFound
	}

	b, err := pprof.GetProfileData(ctx, req.ProfileName, int(req.Timeout), int(req.Debug))
	if err != nil {
		return nil, err
	}

	return &ipc.PProfResponse{
		PprofFile: b,
	}, nil
}

func (h *Handler) buildPipeline(ctx context.Context, info *livekit.IngressInfo, wsUrl, token string, extraParams any) (*media.Pipeline, error) {
	ctx, span := tracer.Start(ctx, "Handler.buildPipeline")
	defer span.End()

	// build/verify params
	var p *media.Pipeline
	params, err := params.GetParams(ctx, h.rpcClient, h.conf, info, wsUrl, token, extraParams)
	if err == nil {
		// create the pipeline
		p, err = media.New(ctx, h.conf, params)
	}

	if err != nil {
		if params != nil {
			info = params.CopyInfo()
		}

		info.State.Error = err.Error()
		info.State.Status = livekit.IngressState_ENDPOINT_ERROR
		h.sendUpdate(ctx, info)
		return nil, err
	}

	return p, nil
}

func (h *Handler) sendUpdate(ctx context.Context, info *livekit.IngressInfo) {
	switch info.State.Status {
	case livekit.IngressState_ENDPOINT_ERROR:
		logger.Warnw("ingress failed", errors.New(info.State.Error),
			"ingressID", info.IngressId,
		)
	case livekit.IngressState_ENDPOINT_INACTIVE:
		logger.Infow("ingress complete", "ingressID", info.IngressId)
	default:
		logger.Infow("ingress update", "ingressID", info.IngressId)
	}

	_, err := h.rpcClient.UpdateIngressState(ctx, &rpc.UpdateIngressStateRequest{
		IngressId: info.IngressId,
		State:     info.State,
	})
	if err != nil {
		logger.Errorw("failed to send update", err)
	}
}

func (h *Handler) Kill() {
	h.kill.Break()
}
