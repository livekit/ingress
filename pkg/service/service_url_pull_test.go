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

package service

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
)

type fakeIOInfoClient struct {
	rpc.IOInfoClient
	createErr error
	creates   int
}

func (c *fakeIOInfoClient) CreateIngress(context.Context, *livekit.IngressInfo, ...psrpc.RequestOption) (*rpc.CreateIngressResponse, error) {
	c.creates++
	return &rpc.CreateIngressResponse{}, c.createErr
}

// recordingNotifier cancels the request context from IngressCreated, the last
// step of handleRequest on the URL pull path. That reproduces the real window,
// a caller that gives up while the pod is still working, without depending on
// timing.
type recordingNotifier struct {
	onUpdate        func()
	createdErr      error
	created         []*livekit.IngressInfo
	updates         int
	sessionEndedFor []string
}

func (n *recordingNotifier) IngressCreated(_ context.Context, _ string, info *livekit.IngressInfo) error {
	n.created = append(n.created, proto.Clone(info).(*livekit.IngressInfo))
	if n.onUpdate != nil {
		n.onUpdate()
	}
	return n.createdErr
}

func (n *recordingNotifier) UpdateIngressState(context.Context, string, *livekit.IngressInfo) error {
	n.updates++
	return nil
}

func (n *recordingNotifier) SessionStarted(context.Context, string, *livekit.IngressInfo) {}

func (n *recordingNotifier) SessionEnded(_ context.Context, resourceID string) {
	n.sessionEndedFor = append(n.sessionEndedFor, resourceID)
}

// A URL pull request whose caller has already given up must not spawn a
// handler, and must report the session it announced as ended.
func TestHandleURLPublishRequestAbandonedByCaller(t *testing.T) {
	conf, err := config.NewConfig(`
redis:
  address: localhost:6379
ws_url: ws://localhost:7880
api_key: key
api_secret: secret
cpu_cost:
  url_cpu_cost: 0.0001
`)
	require.NoError(t, err)

	monitor := stats.NewMonitor()
	require.NoError(t, monitor.Start(conf))
	t.Cleanup(monitor.Stop)

	// The monitor reports no capacity until its first CPU sample lands, and
	// handleNewPublisher rejects before reaching the code under test.
	require.Eventually(t, func() bool { return monitor.GetAvailableCPU() > 0 },
		10*time.Second, 50*time.Millisecond, "cpu stats never became available")

	var spawned int
	newCmd := func(context.Context, *params.Params) (*exec.Cmd, error) {
		spawned++
		return exec.Command("true"), nil
	}

	bus := psrpc.NewLocalMessageBus()
	sm := NewSessionManager(monitor, nil)
	notifier := &recordingNotifier{}
	manager, err := NewProcessManager(sm, notifier, bus, newCmd)
	require.NoError(t, err)

	svc := &Service{
		conf:          conf,
		monitor:       monitor,
		manager:       manager,
		sm:            sm,
		psrpcClient:   &fakeIOInfoClient{},
		stateNotifier: notifier,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notifier.onUpdate = cancel

	_, err = svc.HandleURLPublishRequest(ctx, "res_test", "proj_test", &rpc.StartIngressRequest{
		Info: &livekit.IngressInfo{
			IngressId:           "IN_test",
			InputType:           livekit.IngressInput_URL_INPUT,
			Url:                 "http://example.com/stream.mp4",
			RoomName:            "room",
			ParticipantIdentity: "ident",
		},
		WsUrl: "ws://localhost:7880",
		Token: "token",
	})

	require.Error(t, err, "an abandoned request must not report success")
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, spawned, "no handler may be spawned for a caller that has gone")
	require.Equal(t, []string{"res_test"}, notifier.sessionEndedFor,
		"the session announced by the create must be reported ended")

	require.Len(t, notifier.created, 1)
	state := notifier.created[0].State
	require.Equal(t, livekit.IngressState_ENDPOINT_BUFFERING, state.Status)
	require.NotZero(t, state.UpdatedAt, "the create carries the session's first state")
	require.Zero(t, notifier.updates, "the create announces the session, so no update follows it")
}

func TestSendUpdateURLPull(t *testing.T) {
	newInfo := func() *livekit.IngressInfo {
		return &livekit.IngressInfo{IngressId: "IN_test", State: &livekit.IngressState{ResourceId: "res_test"}}
	}

	t.Run("creates then announces", func(t *testing.T) {
		client, notifier := &fakeIOInfoClient{}, &recordingNotifier{}
		svc := &Service{psrpcClient: client, stateNotifier: notifier}
		require.NoError(t, svc.sendUpdate(context.Background(), "proj", livekit.IngressInput_URL_INPUT, newInfo(), nil))
		require.Equal(t, 1, client.creates)
		require.Len(t, notifier.created, 1)
		require.Zero(t, notifier.updates)
	})

	t.Run("a failed create is not announced", func(t *testing.T) {
		client, notifier := &fakeIOInfoClient{createErr: psrpc.NewErrorf(psrpc.Internal, "boom")}, &recordingNotifier{}
		svc := &Service{psrpcClient: client, stateNotifier: notifier}
		require.Error(t, svc.sendUpdate(context.Background(), "proj", livekit.IngressInput_URL_INPUT, newInfo(), nil))
		require.Empty(t, notifier.created)
		require.Zero(t, notifier.updates)
	})

	t.Run("a failed announce does not fail the create", func(t *testing.T) {
		client, notifier := &fakeIOInfoClient{}, &recordingNotifier{createdErr: errors.New("boom")}
		svc := &Service{psrpcClient: client, stateNotifier: notifier}
		require.NoError(t, svc.sendUpdate(context.Background(), "proj", livekit.IngressInput_URL_INPUT, newInfo(), nil))
		require.Len(t, notifier.created, 1)
	})
}
