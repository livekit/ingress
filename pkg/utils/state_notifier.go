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
	UpdateIngressState(ctx context.Context, projectID string, info *livekit.IngressInfo) error

	// EnsureTerminal reports that a session is over and that nothing will send
	// another update for it, whatever its last state update said. It is called
	// once the handler is gone and its transport with it, so an implementation
	// holding per-session state can finalize that state even when the session's
	// own terminal update never arrived -- a killed handler runs no deferred
	// code, so it never sends one.
	EnsureTerminal(ctx context.Context, resourceID string)
}

type serviceStateNotifier struct {
	psrpcClient rpc.IOInfoClient
}

func NewServiceStateNotifier(psrpcClient rpc.IOInfoClient) StateNotifier {
	return &serviceStateNotifier{
		psrpcClient: psrpcClient,
	}
}

func (sn *serviceStateNotifier) UpdateIngressState(ctx context.Context, _ string, info *livekit.IngressInfo) error {
	req := &rpc.UpdateIngressStateRequest{
		IngressId: info.IngressId,
		State:     info.State,
	}

	_, err := sn.psrpcClient.UpdateIngressState(ctx, req)

	return err
}

// These forward every update onward and hold no per-session state of their
// own, so there is nothing to finalize.
func (sn *serviceStateNotifier) EnsureTerminal(_ context.Context, _ string) {}

type handlerStateNotifier struct {
	ipcClient ipc.IngressServiceClient
}

func NewHandlerStateNotifier(ipcClient ipc.IngressServiceClient) StateNotifier {
	return &handlerStateNotifier{
		ipcClient: ipcClient,
	}
}

func (sn *handlerStateNotifier) UpdateIngressState(ctx context.Context, projectID string, info *livekit.IngressInfo) error {
	req := &ipc.UpdateIngressStateRequest{
		ProjectId: projectID,
		Info:      info,
	}

	_, err := sn.ipcClient.UpdateIngressState(ctx, req)

	return err
}

// These forward every update onward and hold no per-session state of their
// own, so there is nothing to finalize.
func (sn *handlerStateNotifier) EnsureTerminal(_ context.Context, _ string) {}

type noopStateNotifier struct {
}

func NewNoopStateNotifier() StateNotifier {
	return &noopStateNotifier{}
}

func (sn *noopStateNotifier) UpdateIngressState(_ context.Context, _ string, _ *livekit.IngressInfo) error {
	return nil
}

// These forward every update onward and hold no per-session state of their
// own, so there is nothing to finalize.
func (sn *noopStateNotifier) EnsureTerminal(_ context.Context, _ string) {}
