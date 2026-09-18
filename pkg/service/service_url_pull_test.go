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
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
)

// Only CreateIngress is reached on the URL pull path; the embedded nil
// interface satisfies the rest.
type fakeIOInfoClient struct {
	rpc.IOInfoClient
}

func (c *fakeIOInfoClient) CreateIngress(context.Context, *livekit.IngressInfo, ...psrpc.RequestOption) (*rpc.CreateIngressResponse, error) {
	return &rpc.CreateIngressResponse{}, nil
}

// recordingNotifier cancels the request context from UpdateIngressState, which
// sendUpdate calls as the last step of handleRequest. That reproduces the real
// window, a caller that gives up while the pod is still working, without
// depending on timing.
type recordingNotifier struct {
	onUpdate        func()
	sessionEndedFor []string
}

func (n *recordingNotifier) UpdateIngressState(context.Context, string, *livekit.IngressInfo) error {
	if n.onUpdate != nil {
		n.onUpdate()
	}
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
		"the session announced by the first state update must be reported ended")
}
