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
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/types"
)

type fakeRelayServer struct {
	associated   bool
	resourceId   string
	kind         types.StreamKind
	token        string
	dissociated  bool
	payload      []byte
	associateErr error
}

func (f *fakeRelayServer) AssociateRelay(resourceId string, kind types.StreamKind, token string, w io.WriteCloser) error {
	f.associated = true
	f.resourceId = resourceId
	f.kind = kind
	f.token = token

	if f.associateErr != nil {
		return f.associateErr
	}

	_, err := w.Write(f.payload)
	if err != nil {
		return err
	}

	return w.Close()
}

func (f *fakeRelayServer) DissociateRelay(resourceId string, kind types.StreamKind) {
	f.dissociated = true
}

// waitForGoroutines waits for the goroutine count to fall back to target.
// require.Eventually is unusable here, as it runs the condition in goroutines
// of its own.
func waitForGoroutines(target int) bool {
	for i := 0; i < 500; i++ {
		if runtime.NumGoroutine() <= target {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}

	return false
}

func TestRelayHandlerPathParsing(t *testing.T) {
	for _, tc := range []struct {
		name       string
		path       string
		resourceId string
		kind       types.StreamKind
	}{
		{
			name:       "audio",
			path:       "/whip/WH_resource/audio",
			resourceId: "WH_resource",
			kind:       types.Audio,
		},
		{
			name:       "video",
			path:       "/whip/WH_resource/video",
			resourceId: "WH_resource",
			kind:       types.Video,
		},
		{
			// The prefix must be trimmed as a whole, not as a set of
			// characters, or a resource id starting with one of them gets
			// eaten along with the prefix.
			name:       "resource id starting with prefix characters",
			path:       "/whip/whip_resource/audio",
			resourceId: "whip_resource",
			kind:       types.Audio,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRelayServer{payload: []byte("relayed")}
			h := &WHIPRelayHandler{whipServer: f}

			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path+"?token=the-token", nil))

			require.True(t, f.associated)
			require.Equal(t, tc.resourceId, f.resourceId)
			require.Equal(t, tc.kind, f.kind)
			require.Equal(t, "the-token", f.token)
			require.True(t, f.dissociated)

			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, "relayed", w.Body.String())
		})
	}
}

func TestRelayHandlerInvalidPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "missing kind", path: "/whip/WH_resource"},
		{name: "missing resource id", path: "/whip/"},
		{name: "extra element", path: "/whip/WH_resource/audio/extra"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRelayServer{}
			h := &WHIPRelayHandler{whipServer: f}

			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))

			require.False(t, f.associated)
			require.Equal(t, http.StatusNotFound, w.Code)
		})
	}
}

func TestRelayHandlerAssociateError(t *testing.T) {
	f := &fakeRelayServer{associateErr: errors.ErrIngressNotFound}
	h := &WHIPRelayHandler{whipServer: f}

	before := runtime.NumGoroutine()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/whip/WH_resource/audio", nil))

	require.True(t, f.associated)
	require.True(t, f.dissociated)
	require.Equal(t, http.StatusNotFound, w.Code)

	// The relaying goroutine must not stay blocked reporting its result when
	// the association failed and nobody is left to read it.
	require.True(t, waitForGoroutines(before), "relaying goroutine leaked")
}
