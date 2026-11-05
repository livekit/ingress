// Copyright 2023 LiveKit, Inc.
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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/types"
)

// A relay that never answers left Start blocked forever, so pipeline shutdown
// could not end the handler process.
func TestStartReturnsWhenCanceledBeforeRelayAnswers(t *testing.T) {
	gst.Init()

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	w, err := newWHIPAppSource(context.Background(), "WH_test", types.Audio, webrtc.MimeTypeOpus, srv.URL)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx, func(ts time.Duration) time.Duration { return ts }, nil) }()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after cancellation")
	}
}

func TestGetCapsForCodecH264UsesAccessUnitAlignment(t *testing.T) {
	gst.Init()

	caps, err := getCapsForCodec(webrtc.MimeTypeH264)
	require.NoError(t, err)
	require.NotNil(t, caps)

	structure := caps.GetStructure(0)
	require.NotNil(t, structure)
	require.Equal(t, "video/x-h264", structure.GetName())

	require.Equal(t, "byte-stream", structure.GetString("stream-format"))

	require.Equal(t, "au", structure.GetString("alignment"))
}
