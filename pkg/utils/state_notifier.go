// Copyright 2026 LiveKit, Inc.
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

package utils

import (
	"context"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"

	"github.com/livekit/ingress/pkg/ipc"
)

type StateNotifier interface {
	UpdateIngressState(ctx context.Context, projectID string, ingressID string, state *livekit.IngressState) error
}

type serviceStateNotifier struct {
	psrpcClient rpc.IOInfoClient
}

func NewServiceStateNotifier(psrpcClient rpc.IOInfoClient) StateNotifier {
	return &serviceStateNotifier{
		psrpcClient: psrpcClient,
	}
}

func (sn *serviceStateNotifier) UpdateIngressState(ctx context.Context, projectID string, ingressID string, state *livekit.IngressState) error {
	req := &rpc.UpdateIngressStateRequest{
		IngressId: ingressID,
		State:     state,
	}

	_, err := sn.psrpcClient.UpdateIngressState(ctx, req)

	return err
}

type handlerStateNotifier struct {
	ipcClient ipc.IngressServiceClient
}

func NewHandlerStateNotifier(ipcClient ipc.IngressServiceClient) StateNotifier {
	return &handlerStateNotifier{
		ipcClient: ipcClient,
	}
}

func (sn *handlerStateNotifier) UpdateIngressState(ctx context.Context, projectID string, ingressID string, state *livekit.IngressState) error {
	req := &ipc.UpdateIngressStateRequest{
		ProjectId: projectID,
		IngressId: ingressID,
		State:     state,
	}

	_, err := sn.ipcClient.UpdateIngressState(ctx, req)

	return err
}

type noopStateNotifier struct {
}

func NewNoopStateNotifier() StateNotifier {
	return &noopStateNotifier{}
}

func (sn *noopStateNotifier) UpdateIngressState(ctx context.Context, projectID string, ingressID string, state *livekit.IngressState) error {
	return nil
}
