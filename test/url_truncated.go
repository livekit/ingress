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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
// failures into an EOS indistinguishable from a source that ended, so an
// unclassified end here reads downstream as a stream that simply finished.
// truncatedHLS serves a VOD playlist whose later fragments answer 500, so the
// pull stops short of the duration the playlist advertises.
type truncatedHLS struct {
	segments int
	good     int
}

func (*truncatedHLS) pulls() bool { return true }

func (s *truncatedHLS) url(t *testing.T, _ *Runner, _ string) string {
	url := serveTruncatedHLS(t, writeHLSFixture(t, s.segments), s.good)
	logger.Infow("truncated http pull url", "url", url,
		"goodSegments", s.good, "totalSegments", s.segments)

	return url
}

func (*truncatedHLS) publish(*testing.T, *livekit.IngressInfo) {}

// endStream is nothing to do: the origin failing part way through is what ends
// this source.
func (*truncatedHLS) endStream(*testing.T) {}
