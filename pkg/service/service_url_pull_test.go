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
	"fmt"
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

// Only CreateIngress is reached on the URL pull path; the embedded nil
// interface satisfies the rest.
type fakeIOInfoClient struct {
	rpc.IOInfoClient
	created []*livekit.IngressInfo
}

func (c *fakeIOInfoClient) CreateIngress(_ context.Context, info *livekit.IngressInfo, _ ...psrpc.RequestOption) (*rpc.CreateIngressResponse, error) {
	c.created = append(c.created, proto.Clone(info).(*livekit.IngressInfo))
	return &rpc.CreateIngressResponse{}, nil
}

// recordingNotifier cancels the request context from UpdateIngressState or
// IngressCreated, one of which is the last step of handleRequest. That
// reproduces the real window, a caller that gives up while the pod is still
// working, without depending on timing.
type recordingNotifier struct {
	onUpdate        func()
	updates         int
	creates         int
	sessionEndedFor []string
}

func (n *recordingNotifier) UpdateIngressState(context.Context, string, *livekit.IngressInfo) error {
	n.updates++
	if n.onUpdate != nil {
		n.onUpdate()
	}
	return nil
}

func (n *recordingNotifier) IngressCreated(context.Context, string, *livekit.IngressInfo) error {
	n.creates++
	if n.onUpdate != nil {
		n.onUpdate()
	}
	return nil
}

func (n *recordingNotifier) SessionStarted(context.Context, string, *livekit.IngressInfo) {}

func (n *recordingNotifier) SessionEnded(_ context.Context, resourceID string) {
	n.sessionEndedFor = append(n.sessionEndedFor, resourceID)
}

func newURLPullTestService(t *testing.T, extraConf string) (*Service, *fakeIOInfoClient, *recordingNotifier, *int) {
	conf, err := config.NewConfig(`
redis:
  address: localhost:6379
ws_url: ws://localhost:7880
api_key: key
api_secret: secret
cpu_cost:
  url_cpu_cost: 0.0001
` + extraConf)
	require.NoError(t, err)

	monitor := stats.NewMonitor()
	require.NoError(t, monitor.Start(conf))
	t.Cleanup(monitor.Stop)

	// The monitor reports no capacity until its first CPU sample lands, and
	// handleNewPublisher rejects before reaching the code under test.
	require.Eventually(t, func() bool { return monitor.GetAvailableCPU() > 0 },
		10*time.Second, 50*time.Millisecond, "cpu stats never became available")

	spawned := new(int)
	newCmd := func(context.Context, *params.Params) (*exec.Cmd, error) {
		*spawned++
		return exec.Command("true"), nil
	}

	bus := psrpc.NewLocalMessageBus()
	sm := NewSessionManager(monitor, nil)
	notifier := &recordingNotifier{}
	manager, err := NewProcessManager(sm, notifier, bus, newCmd)
	require.NoError(t, err)

	ioClient := &fakeIOInfoClient{}
	svc := &Service{
		conf:          conf,
		monitor:       monitor,
		manager:       manager,
		sm:            sm,
		psrpcClient:   ioClient,
		stateNotifier: notifier,
	}
	return svc, ioClient, notifier, spawned
}

// handleURLPublishRequestAbandoned runs a URL pull request whose caller gives up
// at the last step of handleRequest.
func handleURLPublishRequestAbandoned(t *testing.T, svc *Service, notifier *recordingNotifier) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notifier.onUpdate = cancel

	_, err := svc.HandleURLPublishRequest(ctx, "res_test", "proj_test", &rpc.StartIngressRequest{
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
	return err
}

// A URL pull request whose caller has already given up must not spawn a
// handler, and must report the session it announced as ended.
func TestHandleURLPublishRequestAbandonedByCaller(t *testing.T) {
	svc, _, notifier, spawned := newURLPullTestService(t, "")

	err := handleURLPublishRequestAbandoned(t, svc, notifier)

	require.Error(t, err, "an abandoned request must not report success")
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, *spawned, "no handler may be spawned for a caller that has gone")
	require.Equal(t, []string{"res_test"}, notifier.sessionEndedFor,
		"the session announced by the first state update must be reported ended")
}

// With create_persists_state, the create carries the session's first state and
// the notifier is told the session exists without an update being sent.
func TestHandleURLPublishRequestCreatePersistsState(t *testing.T) {
	for _, persists := range []bool{false, true} {
		t.Run(fmt.Sprintf("create_persists_state=%v", persists), func(t *testing.T) {
			svc, ioClient, notifier, _ := newURLPullTestService(t, fmt.Sprintf("create_persists_state: %v\n", persists))

			require.ErrorIs(t, handleURLPublishRequestAbandoned(t, svc, notifier), context.Canceled)

			require.Len(t, ioClient.created, 1)
			state := ioClient.created[0].State
			require.Equal(t, livekit.IngressState_ENDPOINT_BUFFERING, state.Status)
			require.Equal(t, "res_test", state.ResourceId)
			if persists {
				require.NotZero(t, state.UpdatedAt, "the create stands in for the update, so it carries its timestamp")
				require.Zero(t, notifier.updates)
				require.Equal(t, 1, notifier.creates)
			} else {
				require.Equal(t, 1, notifier.updates)
				require.Zero(t, notifier.creates)
			}
		})
	}
}
