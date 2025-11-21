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

package service

import (
	"context"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	google_protobuf2 "google.golang.org/protobuf/types/known/emptypb"

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/ipc"
	"github.com/livekit/ingress/pkg/media"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/pprof"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/tracer"
)

type Handler struct {
	ipc.UnimplementedIngressHandlerServer

	conf      *config.Config
	pipeline  *media.Pipeline
	rpcClient rpc.IOInfoClient

	statsGatherer *stats.LocalMediaStatsGatherer

	grpcServer *grpc.Server
	kill       core.Fuse
	done       core.Fuse
}

func NewHandler(conf *config.Config, rpcClient rpc.IOInfoClient) *Handler {
	return &Handler{
		conf:          conf,
		rpcClient:     rpcClient,
		statsGatherer: stats.NewLocalMediaStatsGatherer(),
		grpcServer:    grpc.NewServer(),
	}
}

func (h *Handler) HandleIngress(ctx context.Context, info *livekit.IngressInfo, wsUrl, token, relayToken string, featureFlags map[string]string, loggingFields map[string]string, extraParams any) error {
	ctx, span := tracer.Start(ctx, "Handler.HandleRequest")
	defer span.End()

	p, err := h.buildPipeline(ctx, info, wsUrl, token, relayToken, featureFlags, loggingFields, extraParams)
	if err != nil {
		span.RecordError(err)
		return err
	}
	h.pipeline = p

	defer func() {
		switch err {
		case nil:
			if p.Reusable {
				p.SetStatus(livekit.IngressState_ENDPOINT_INACTIVE, nil)
			} else {
				p.SetStatus(livekit.IngressState_ENDPOINT_COMPLETE, nil)
			}
		default:
			span.RecordError(err)
			p.SetStatus(livekit.IngressState_ENDPOINT_ERROR, err)
		}

		p.SendStateUpdate(ctx)
	}()

	listener, err := net.Listen(network, getSocketAddress(p.TmpDir))
	if err != nil {
		span.RecordError(err)
		logger.Errorw("failed starting grpc listener", err)
		return err
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
	result := make(chan error, 1)
	go func() {
		err := p.Run(ctx)
		result <- err
		h.done.Break()
	}()

	kill := h.kill.Watch()

	for {
		select {
		case <-kill:
			// kill signal received
			p.SendEOS(ctx)
			kill = nil

		case err = <-result:
			// ingress finished
			return err
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

func (h *Handler) ICERestartWHIPResource(ctx context.Context, req *rpc.ICERestartWHIPResourceRequest) (*rpc.ICERestartWHIPResourceResponse, error) {
	_, span := tracer.Start(ctx, "Handler.ICERestartWHIPResource")
	defer span.End()

	return &rpc.ICERestartWHIPResourceResponse{}, nil
}

func (h *Handler) WHIPRTCConnectionNotify(ctx context.Context, req *rpc.WHIPRTCConnectionNotifyRequest) (*google_protobuf2.Empty, error) {
	_, span := tracer.Start(ctx, "Handler.WHIPRTCConnectionNotify")
	defer span.End()

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

func (h *Handler) GetPipelineDot(ctx context.Context, in *ipc.GstPipelineDebugDotRequest) (*ipc.GstPipelineDebugDotResponse, error) {
	ctx, span := tracer.Start(ctx, "Handler.GetPipelineDot")
	defer span.End()

	if h.pipeline == nil {
		return nil, errors.ErrIngressNotFound
	}

	res := make(chan string, 1)
	go func() {
		res <- h.pipeline.GetGstPipelineDebugDot()
	}()

	select {
	case r := <-res:
		return &ipc.GstPipelineDebugDotResponse{
			DotFile: r,
		}, nil

	case <-time.After(2 * time.Second):
		return nil, status.New(codes.DeadlineExceeded, "timed out requesting pipeline debug info").Err()
	}

}

func (h *Handler) GatherMediaStats(ctx context.Context, in *ipc.GatherMediaStatsRequest) (*ipc.GatherMediaStatsResponse, error) {
	st, err := h.statsGatherer.GatherStats(ctx)
	if err != nil {
		return nil, err
	}

	return &ipc.GatherMediaStatsResponse{
		Stats: st,
	}, nil
}

func (h *Handler) UpdateMediaStats(ctx context.Context, in *ipc.UpdateMediaStatsRequest) (*google_protobuf2.Empty, error) {
	ctx, span := tracer.Start(ctx, "Handler.UpdateMediaStats")
	defer span.End()

	if h.pipeline == nil {
		return &google_protobuf2.Empty{}, nil
	}

	if in.Stats == nil {
		return &google_protobuf2.Empty{}, nil
	}

	lsu := &stats.LocalStatsUpdater{
		Params: h.pipeline.Params,
	}

	err := lsu.UpdateMediaStats(ctx, in.Stats)
	if err != nil {
		return nil, err
	}

	return &google_protobuf2.Empty{}, nil
}

func (h *Handler) buildPipeline(ctx context.Context, info *livekit.IngressInfo, wsUrl, token, relayToken string, featureFlags map[string]string, loggingFields map[string]string, extraParams any) (*media.Pipeline, error) {
	ctx, span := tracer.Start(ctx, "Handler.buildPipeline")
	defer span.End()

	// build/verify params
	var p *media.Pipeline
	params, err := params.GetParams(ctx, h.rpcClient, h.conf, info, wsUrl, token, relayToken, featureFlags, loggingFields, extraParams)
	if err == nil {
		// create the pipeline
		p, err = media.New(ctx, h.conf, params, h.statsGatherer)
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

	info.State.UpdatedAt = time.Now().UnixNano()

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
