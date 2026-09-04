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

//go:build integration

package test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"

	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/service"
	"github.com/livekit/ingress/pkg/utils"
)

const (
	truncatedFixtureSegments = 10
	truncatedGoodSegments    = 3
)

// writeHLSFixture writes a VOD playlist and its segments into a temporary
// directory. hlssink2 produces both, so no media is checked in.
func writeHLSFixture(t *testing.T, segments int) string {
	t.Helper()

	gst.Init(nil)

	dir := t.TempDir()

	// One buffer per second at the default rate, so num-buffers is roughly the
	// segment count. max-files=0 keeps every segment; the default prunes them.
	enc, err := gst.NewPipelineFromString(fmt.Sprintf(
		"audiotestsrc num-buffers=%d samplesperbuffer=44100 ! audioconvert ! avenc_aac ! aacparse "+
			"! hlssink2 location=%s/seg%%05d.ts playlist-location=%s/playlist.m3u8 "+
			"target-duration=1 playlist-length=0 max-files=0",
		segments, dir, dir))
	require.NoError(t, err)
	require.NoError(t, enc.BlockSetState(gst.StatePlaying))

	msg := enc.GetPipelineBus().TimedPopFiltered(
		gst.ClockTime(60*time.Second), gst.MessageEOS|gst.MessageError)
	require.NotNil(t, msg, "writing the fixture timed out")
	require.Equal(t, gst.MessageEOS, msg.Type(), msg.String())
	require.NoError(t, enc.BlockSetState(gst.StateNull))

	playlist, err := os.ReadFile(filepath.Join(dir, "playlist.m3u8"))
	require.NoError(t, err)
	require.Contains(t, string(playlist), "#EXT-X-ENDLIST", "the fixture must be a VOD playlist")

	return dir
}

// serveTruncatedHLS serves the fixture and answers fragment requests with 500
// once goodSegments have been handed out, which is the CDN outage this test is
// about. The playlist keeps returning 200, so the only failure the demuxer sees
// is on a fragment, and that is what it turns into an EOS.
func serveTruncatedHLS(t *testing.T, dir string, goodSegments int) string {
	t.Helper()

	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Base(r.URL.Path)
		if !strings.HasSuffix(name, ".m3u8") && int(served.Add(1)) > goodSegments {
			http.Error(w, "upstream unavailable", http.StatusInternalServerError)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, name))
	}))
	t.Cleanup(srv.Close)

	return srv.URL + "/playlist.m3u8"
}

// RunURLTruncatedTest pulls an HLS source whose origin fails part way through
// and requires the ingress to report an error. GStreamer converts the fragment
// failures into an EOS indistinguishable from a source that ended, so before
// this was handled the ingress reported ENDPOINT_COMPLETE and nothing
// downstream could tell the two apart.
//
// The suite needs a message bus of its own. Anything else on the same Redis, a
// livekit-server or a second ingress, registers its own IOInfoServer and takes a
// share of the state updates, which surfaces here as an ingress that never
// reaches a terminal state. A separate Redis instance is required rather than a
// separate database, since psrpc rides pub/sub and pub/sub is not scoped to one.
//
//nolint:revive // TODO(milos) reduce argument count, as with the tests beside this one
func RunURLTruncatedTest(t *testing.T, conf *TestConfig, bus psrpc.MessageBus, psrpcClient rpc.IOInfoClient, sn utils.StateNotifier, newCmd func(ctx context.Context, p *params.Params) (*exec.Cmd, error)) {
	svc, err := service.NewService(conf.Config, psrpcClient, sn, bus, nil, nil, newCmd, "")
	require.NoError(t, err)

	go func() {
		err := svc.Run()
		require.NoError(t, err)
	}()

	t.Cleanup(func() {
		svc.Stop(true)
	})

	_, err = rpc.NewIngressInternalServer(svc, bus)
	require.NoError(t, err)

	internalPsrpcClient, err := rpc.NewIngressInternalClient(bus, psrpc.WithClientTimeout(5*time.Second))
	require.NoError(t, err)

	updates := make(chan *rpc.UpdateIngressStateRequest, 10)
	ios := &ioServer{}
	ios.updateIngressState = func(req *rpc.UpdateIngressStateRequest) error {
		updates <- req
		return nil
	}
	ios.getIngressInfo = func(_ *rpc.GetIngressInfoRequest) (*rpc.GetIngressInfoResponse, error) {
		return nil, psrpc.NewErrorf(psrpc.NotFound, "not found")
	}

	ioPsrpc, err := rpc.NewIOInfoServer(ios, bus)
	require.NoError(t, err)
	t.Cleanup(func() {
		ioPsrpc.Kill()
	})

	url := serveTruncatedHLS(t, writeHLSFixture(t, truncatedFixtureSegments), truncatedGoodSegments)

	// The id is unique per run: the state notifier rejects an update whose
	// ingress is older than one it already holds, and this test leaves its
	// ingress in a terminal state rather than deleting it.
	id := fmt.Sprintf("ingress_id_truncated_%d", time.Now().UnixNano())

	info := &livekit.IngressInfo{
		IngressId:           id,
		InputType:           livekit.IngressInput_URL_INPUT,
		Name:                "ingress-test-truncated",
		RoomName:            conf.RoomName,
		ParticipantIdentity: "ingress-test-truncated",
		ParticipantName:     "ingress-test-truncated",
		StreamKey:           id,
		Url:                 url,
		Audio: &livekit.IngressAudioOptions{
			Name:   "audio",
			Source: 0,
			EncodingOptions: &livekit.IngressAudioOptions_Options{
				Options: &livekit.IngressAudioEncodingOptions{
					AudioCodec: livekit.AudioCodec_OPUS,
					Bitrate:    64000,
					DisableDtx: false,
					Channels:   2,
				},
			},
		},
	}

	time.Sleep(time.Second)

	logger.Infow("truncated http pull url", "url", info.Url,
		"goodSegments", truncatedGoodSegments, "totalSegments", truncatedFixtureSegments)

	started, err := internalPsrpcClient.StartIngress(context.Background(), &rpc.StartIngressRequest{Info: info})
	require.NoError(t, err)
	require.Equal(t, livekit.IngressState_ENDPOINT_BUFFERING, started.State.Status)

	// The pull ends on its own once the origin starts failing, so wait for a
	// terminal state rather than deleting the ingress, which would be a stop we
	// asked for and is deliberately not classified.
	deadline := time.After(90 * time.Second)
	for {
		select {
		case update := <-updates:
			switch update.State.Status {
			case livekit.IngressState_ENDPOINT_ERROR:
				require.Contains(t, update.State.Error, "source ended after",
					"the error must say the source stopped short, not something incidental")
				return
			case livekit.IngressState_ENDPOINT_COMPLETE, livekit.IngressState_ENDPOINT_INACTIVE:
				t.Fatalf("a truncated pull was reported as %s: this is the CS-1593 regression",
					update.State.Status)
			}
		case <-deadline:
			t.Fatal("the ingress never reached a terminal state")
		}
	}
}
