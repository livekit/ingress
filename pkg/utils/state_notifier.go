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
	// IngressCreated announces a URL pull session whose ingress was just
	// created with its first state, as that state's update would.
	IngressCreated(ctx context.Context, projectID string, info *livekit.IngressInfo) error

	UpdateIngressState(ctx context.Context, projectID string, info *livekit.IngressInfo) error

	// SessionStarted reports that a session is running. It does not mark the
	// first the notifier hears of one: a session is announced by IngressCreated
	// or its first state update, which carry ENDPOINT_BUFFERING and are sent
	// before this call on every path. What this marks is the point from which the session
	// is live, so anything an implementation meters belongs between here and
	// SessionEnded rather than from whenever an update first arrived.
	SessionStarted(ctx context.Context, projectID string, info *livekit.IngressInfo)

	// SessionEnded reports that a session is over and that nothing more will be
	// reported for it. It is called wherever a session stops running, whether
	// it ended, failed to start, or had its handler killed, so an
	// implementation can release whatever it holds without waiting for a
	// terminal update that may never arrive.
	SessionEnded(ctx context.Context, resourceID string)
}

type serviceStateNotifier struct {
	psrpcClient rpc.IOInfoClient
}

func NewServiceStateNotifier(psrpcClient rpc.IOInfoClient) StateNotifier {
	return &serviceStateNotifier{
		psrpcClient: psrpcClient,
	}
}

func (sn *serviceStateNotifier) IngressCreated(ctx context.Context, projectID string, info *livekit.IngressInfo) error {
	return sn.UpdateIngressState(ctx, projectID, info)
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
// own, so there is nothing to track.
func (sn *serviceStateNotifier) SessionStarted(context.Context, string, *livekit.IngressInfo) {}
func (sn *serviceStateNotifier) SessionEnded(context.Context, string)                         {}

type handlerStateNotifier struct {
	ipcClient ipc.IngressServiceClient
}

func NewHandlerStateNotifier(ipcClient ipc.IngressServiceClient) StateNotifier {
	return &handlerStateNotifier{
		ipcClient: ipcClient,
	}
}

// Handlers are started for an ingress that already exists.
func (sn *handlerStateNotifier) IngressCreated(context.Context, string, *livekit.IngressInfo) error {
	return nil
}

func (sn *handlerStateNotifier) UpdateIngressState(ctx context.Context, projectID string, info *livekit.IngressInfo) error {
	req := &ipc.UpdateIngressStateRequest{
		ProjectId: projectID,
		Info:      info,
	}

	_, err := sn.ipcClient.UpdateIngressState(ctx, req)

	return err
}

func (sn *handlerStateNotifier) SessionStarted(context.Context, string, *livekit.IngressInfo) {}
func (sn *handlerStateNotifier) SessionEnded(context.Context, string)                         {}

type noopStateNotifier struct {
}

func NewNoopStateNotifier() StateNotifier {
	return &noopStateNotifier{}
}

func (sn *noopStateNotifier) IngressCreated(_ context.Context, _ string, _ *livekit.IngressInfo) error {
	return nil
}

func (sn *noopStateNotifier) UpdateIngressState(_ context.Context, _ string, _ *livekit.IngressInfo) error {
	return nil
}

func (sn *noopStateNotifier) SessionStarted(context.Context, string, *livekit.IngressInfo) {}
func (sn *noopStateNotifier) SessionEnded(context.Context, string)                         {}
