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

package whip

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/types"
)

// fakeWHIPHandler starts at once and whose session is already over, the way a
// session looks when the SFU breaks its done channel right after the SDP
// exchange.
type fakeWHIPHandler struct{}

func (fakeWHIPHandler) Init(context.Context, string) (*WHIPInitResponse, error) {
	return &WHIPInitResponse{}, nil
}

func (fakeWHIPHandler) SetMediaStatsGatherer(*stats.LocalMediaStatsGatherer) {}

func (fakeWHIPHandler) Start(context.Context) (map[types.StreamKind]string, error) {
	return nil, nil
}

func (fakeWHIPHandler) WaitForSessionEnd(context.Context) error { return nil }

func (fakeWHIPHandler) Close() {}

func (fakeWHIPHandler) AssociateRelay(types.StreamKind, string, io.WriteCloser) error { return nil }

func (fakeWHIPHandler) DissociateRelay(types.StreamKind) {}

// ready registers the session and ended removes it, so ended must not run until
// ready has returned. A session ended first is registered afterwards with
// nothing left to remove it.
func TestReadyReturnsBeforeEnded(t *testing.T) {
	readyReturned := make(chan struct{})
	endedCalled := make(chan struct{})

	s := &WHIPServer{
		ctx:      context.Background(),
		handlers: make(map[string]WHIPHandler),
		newHandler: func(*params.Params, string, string, string) (WHIPHandler, error) {
			return fakeWHIPHandler{}, nil
		},
	}
	s.onPublish = func(_, resourceId string) (*params.Params, func(map[types.StreamKind]string, error) *stats.LocalMediaStatsGatherer, func(error), error) {
		p := &params.Params{IngressInfo: &livekit.IngressInfo{
			IngressId:         "in_test",
			EnableTranscoding: proto.Bool(false),
			State:             &livekit.IngressState{ResourceId: resourceId},
		}}

		ready := func(map[types.StreamKind]string, error) *stats.LocalMediaStatsGatherer {
			defer close(readyReturned)

			// The real ready sends a state update before it registers the
			// session. Stand in for that round trip, giving ended the chance
			// to overtake it if anything has already started it.
			select {
			case <-endedCalled:
				t.Error("ended ran while ready was still in progress")
			case <-time.After(100 * time.Millisecond):
			}
			return nil
		}

		ended := func(error) {
			select {
			case <-readyReturned:
			default:
				t.Error("ended ran before ready returned")
			}
			close(endedCalled)
		}

		return p, ready, ended, nil
	}

	_, _, err := s.createStream("stream_key", "sdp_offer", "test")
	require.NoError(t, err)

	for _, ch := range []chan struct{}{readyReturned, endedCalled} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("session did not run to its end")
		}
	}
}
