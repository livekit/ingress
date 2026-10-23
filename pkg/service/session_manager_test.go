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
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/ipc"
	"github.com/livekit/ingress/pkg/stats"
)

type fakeSessionAPI struct {
	closed int
}

func (f *fakeSessionAPI) UpdateMediaStats(context.Context, *ipc.MediaStats) error { return nil }

func (f *fakeSessionAPI) GatherStats(context.Context) (*ipc.MediaStats, error) { return nil, nil }

func (f *fakeSessionAPI) GetProfileData(context.Context, string, int32, int32) ([]byte, error) {
	return nil, nil
}

func (f *fakeSessionAPI) GetPipelineDot(context.Context) (string, error) { return "", nil }

func (f *fakeSessionAPI) CloseSession(context.Context) { f.closed++ }

func newTestSessionManager(t *testing.T) *SessionManager {
	t.Helper()

	conf, err := config.NewConfig(`
redis:
  address: localhost:6379
ws_url: ws://localhost:7880
api_key: key
api_secret: secret
cpu_cost:
  rtmp_cpu_cost: 0.0001
  whip_cpu_cost: 0.0001
  whip_bypass_transcoding_cpu_cost: 0.0001
  url_cpu_cost: 0.0001
`)
	require.NoError(t, err)

	monitor := stats.NewMonitor()
	require.NoError(t, monitor.Start(conf))
	t.Cleanup(monitor.Stop)

	return NewSessionManager(monitor, nil)
}

func testIngressInfo(resourceID string) *livekit.IngressInfo {
	return &livekit.IngressInfo{
		IngressId:         "in_session_manager",
		InputType:         livekit.IngressInput_WHIP_INPUT,
		EnableTranscoding: proto.Bool(false),
		State:             &livekit.IngressState{ResourceId: resourceID},
	}
}

func (sm *SessionManager) endedCount(t *testing.T) int {
	t.Helper()

	sm.lock.Lock()
	defer sm.lock.Unlock()

	return len(sm.ended) + len(sm.endedOlder)
}

// A session whose end is processed before its start must leave nothing behind.
// A registered session is only ever removed by an end, so one installed after
// its own end is unreachable: it holds the node's idle check false forever and
// keeps the ingress in ListIngress with no way to stop it.
func TestIngressEndedBeforeStarted(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.IngressEnded("WH_late")
	sm.IngressStarted(testIngressInfo("WH_late"), &fakeSessionAPI{})

	require.True(t, sm.IsIdle(), "node must be idle, otherwise it can never drain")
	require.Empty(t, sm.ListIngress(), "ingress must not be reported active")

	_, err := sm.GetIngressSessionAPI("WH_late")
	require.ErrorIs(t, err, errors.ErrIngressNotFound)

	// No session record means no media stats reporter, and so no collector
	// goroutine left running for a session that is over.
	_, err = sm.GetIngressMediaStats("WH_late")
	require.ErrorIs(t, err, errors.ErrIngressNotFound)

	require.Zero(t, sm.endedCount(t), "the id must not be held once its start has been discarded")
}

func TestIngressStartedThenEnded(t *testing.T) {
	sm := newTestSessionManager(t)
	api := &fakeSessionAPI{}

	sm.IngressStarted(testIngressInfo("WH_normal"), api)

	require.False(t, sm.IsIdle())
	require.Len(t, sm.ListIngress(), 1)

	sm.IngressEnded("WH_normal")

	require.True(t, sm.IsIdle())
	require.Empty(t, sm.ListIngress())
	require.Equal(t, 1, api.closed, "the session API must be closed exactly once")
	require.Zero(t, sm.endedCount(t), "a session that was registered must not be held")
}

func TestIngressEndedOnlyAffectsItsOwnResource(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.IngressEnded("WH_late")
	sm.IngressStarted(testIngressInfo("WH_other"), &fakeSessionAPI{})

	sessions := sm.ListIngress()
	require.Len(t, sessions, 1)
	require.Equal(t, "WH_other", sessions[0].ResourceId)

	sm.IngressEnded("WH_other")
}

// An id must outlive endedTracked later ends, so a start still in flight is
// matched however many unrelated sessions end while it is on its way.
func TestEndedResourceIDSurvivesLaterEnds(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.IngressEnded("WH_waiting")
	for i := 0; i < endedTracked; i++ {
		sm.IngressEnded(fmt.Sprintf("WH_filler_%d", i))
	}

	sm.IngressStarted(testIngressInfo("WH_waiting"), &fakeSessionAPI{})

	require.True(t, sm.IsIdle())
	require.Empty(t, sm.ListIngress())
}

// Ends that never see their start are the only ones held, and they must not
// accumulate without bound.
func TestEndedResourceIDsAreBounded(t *testing.T) {
	sm := newTestSessionManager(t)

	for i := 0; i < 5*endedTracked; i++ {
		sm.IngressEnded(fmt.Sprintf("WH_%d", i))
	}

	require.LessOrEqual(t, sm.endedCount(t), 2*endedTracked)
}

// Once enough later ends accumulate an id is dropped, and a start for it
// registers the way it would if its end had never been seen.
func TestOldestEndedResourceIDsAreDropped(t *testing.T) {
	sm := newTestSessionManager(t)

	sm.IngressEnded("WH_oldest")
	for i := 0; i < 2*endedTracked; i++ {
		sm.IngressEnded(fmt.Sprintf("WH_filler_%d", i))
	}

	sm.IngressStarted(testIngressInfo("WH_oldest"), &fakeSessionAPI{})

	require.Len(t, sm.ListIngress(), 1)

	sm.IngressEnded("WH_oldest")
}
