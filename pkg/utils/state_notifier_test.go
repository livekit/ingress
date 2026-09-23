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
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
)

type fakeIOInfoClient struct {
	rpc.IOInfoClient
	createErr, updateErr error
	calls                []string
}

func (c *fakeIOInfoClient) CreateIngress(context.Context, *livekit.IngressInfo, ...psrpc.RequestOption) (*rpc.CreateIngressResponse, error) {
	c.calls = append(c.calls, "create")
	return &rpc.CreateIngressResponse{}, c.createErr
}

func (c *fakeIOInfoClient) UpdateIngressState(context.Context, *rpc.UpdateIngressStateRequest, ...psrpc.RequestOption) (*emptypb.Empty, error) {
	c.calls = append(c.calls, "update")
	return &emptypb.Empty{}, c.updateErr
}

func TestServiceStateNotifierCreateIngress(t *testing.T) {
	info := &livekit.IngressInfo{IngressId: "IN_test", State: &livekit.IngressState{ResourceId: "res_test"}}

	t.Run("creates then updates", func(t *testing.T) {
		c := &fakeIOInfoClient{}
		require.NoError(t, NewServiceStateNotifier(c).CreateIngress(context.Background(), "proj", info))
		require.Equal(t, []string{"create", "update"}, c.calls)
	})

	t.Run("a failed create sends no update", func(t *testing.T) {
		c := &fakeIOInfoClient{createErr: psrpc.NewErrorf(psrpc.Internal, "boom")}
		require.Error(t, NewServiceStateNotifier(c).CreateIngress(context.Background(), "proj", info))
		require.Equal(t, []string{"create"}, c.calls)
	})

	t.Run("a failed update does not fail the create", func(t *testing.T) {
		c := &fakeIOInfoClient{updateErr: psrpc.NewErrorf(psrpc.Internal, "boom")}
		require.NoError(t, NewServiceStateNotifier(c).CreateIngress(context.Background(), "proj", info))
		require.Equal(t, []string{"create", "update"}, c.calls)
	})
}
