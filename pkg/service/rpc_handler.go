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

	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/tracer"
	google_protobuf2 "google.golang.org/protobuf/types/known/emptypb"
)

type rpcHandlerContext struct {
	service *Service
	params  *params.Params
}

// IngressHandler RPC interface
func (r rpcHandlerContext) UpdateIngress(ctx context.Context, req *livekit.UpdateIngressRequest) (*livekit.IngressState, error) {
	_, span := tracer.Start(ctx, "whipHandler.UpdateIngress")
	defer span.End()

	r.service.whipSrv.CloseHandler(r.params.CopyInfo().State.ResourceId)

	return r.params.CopyInfo().State, nil
}

func (r rpcHandlerContext) DeleteIngress(ctx context.Context, req *livekit.DeleteIngressRequest) (*livekit.IngressState, error) {
	_, span := tracer.Start(ctx, "whipHandler.DeleteIngress")
	defer span.End()

	r.service.whipSrv.CloseHandler(r.params.CopyInfo().State.ResourceId)

	return r.params.CopyInfo().State, nil
}

func (r rpcHandlerContext) DeleteWHIPResource(ctx context.Context, req *rpc.DeleteWHIPResourceRequest) (*google_protobuf2.Empty, error) {
	_, span := tracer.Start(ctx, "whipHandler.DeleteWHIPResource")
	defer span.End()

	info := r.params.CopyInfo()

	// only test for stream key correctness if it is part of the request for backward compatibility
	if req.StreamKey != "" && info.StreamKey != req.StreamKey {
		r.params.GetLogger().Infow("received delete request with wrong stream key", "streamKey", req.StreamKey)
	}

	r.service.whipSrv.CloseHandler(info.State.ResourceId)

	return &google_protobuf2.Empty{}, nil
}

func RegisterIngressRpcHandlers(server rpc.IngressHandlerServer, info *livekit.IngressInfo) error {
	if err := server.RegisterUpdateIngressTopic(info.IngressId); err != nil {
		return err
	}
	if err := server.RegisterDeleteIngressTopic(info.IngressId); err != nil {
		return err
	}

	if info.InputType == livekit.IngressInput_WHIP_INPUT {
		if err := server.RegisterDeleteWHIPResourceTopic(info.State.ResourceId); err != nil {
			return err
		}
	}

	return nil
}

func DeregisterIngressRpcHandlers(server rpc.IngressHandlerServer, info *livekit.IngressInfo) {
	server.DeregisterUpdateIngressTopic(info.IngressId)
	server.RegisterDeleteIngressTopic(info.IngressId)

	if info.InputType == livekit.IngressInput_WHIP_INPUT {
		server.RegisterDeleteWHIPResourceTopic(info.State.ResourceId)
	}
}

func RegisterListIngress(topic string, srv rpc.IngressInternalServer) error {
	return srv.RegisterListActiveIngressTopic(topic)
}
