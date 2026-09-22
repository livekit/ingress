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

//go:build integration

package test

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
)

// ioServer stands in for the IOInfo service the ingress reports to. Only the
// two ingress methods the suite drives are wired up.
type ioServer struct {
	getIngressInfo     func(*rpc.GetIngressInfoRequest) (*rpc.GetIngressInfoResponse, error)
	updateIngressState func(*rpc.UpdateIngressStateRequest) error
}

func (s *ioServer) GetIngressInfo(_ context.Context, req *rpc.GetIngressInfoRequest) (*rpc.GetIngressInfoResponse, error) {
	return s.getIngressInfo(req)
}

func (s *ioServer) UpdateIngressState(_ context.Context, req *rpc.UpdateIngressStateRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, s.updateIngressState(req)
}

func (s *ioServer) CreateIngress(_ context.Context, info *livekit.IngressInfo) (*rpc.CreateIngressResponse, error) {
	return &rpc.CreateIngressResponse{Info: info}, nil
}

func (s *ioServer) CreateEgress(_ context.Context, _ *livekit.EgressInfo) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (s *ioServer) GetEgress(_ context.Context, _ *rpc.GetEgressRequest) (*livekit.EgressInfo, error) {
	return nil, nil
}

func (s *ioServer) ListEgress(_ context.Context, _ *livekit.ListEgressRequest) (*livekit.ListEgressResponse, error) {
	return nil, nil
}

func (s *ioServer) UpdateEgress(_ context.Context, _ *livekit.EgressInfo) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (s *ioServer) UpdateMetrics(_ context.Context, _ *rpc.UpdateMetricsRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (s *ioServer) EvaluateSIPDispatchRules(context.Context, *rpc.EvaluateSIPDispatchRulesRequest) (*rpc.EvaluateSIPDispatchRulesResponse, error) {
	return nil, nil
}

func (s *ioServer) GetSIPTrunkAuthentication(context.Context, *rpc.GetSIPTrunkAuthenticationRequest) (*rpc.GetSIPTrunkAuthenticationResponse, error) {
	return nil, nil
}

func (s *ioServer) UpdateSIPCallState(context.Context, *rpc.UpdateSIPCallStateRequest) (*emptypb.Empty, error) {
	return nil, nil
}

func (s *ioServer) RecordCallContext(context.Context, *rpc.RecordCallContextRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}
