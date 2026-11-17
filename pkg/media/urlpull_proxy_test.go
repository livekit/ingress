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

package media

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
	"sync"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/gstutil"
	"github.com/livekit/ingress/pkg/media/urlpull"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/protocol/livekit"
)

const (
	urlPullProxyChildEnv = "INGRESS_URL_PULL_PROXY_CHILD"
	urlPullProxyAddrEnv  = "INGRESS_URL_PULL_PROXY_ADDR"

	proxiedSourceURL = "http://origin.invalid/master.m3u8"
)

// Hosts the media playlist spreads its segments over, one kind of address
// each. None of them serves the fixture, so a segment only arrives if the
// request went to the proxy.
var proxiedSegmentHosts = []string{"127.0.0.1:1", "169.254.0.1", "10.255.255.1"}

// newFixtureProxy is a forward proxy that serves the HLS fixture for any
// origin: a master playlist, a media playlist on another host, and segments on
// proxiedSegmentHosts. It returns the URLs of the requests it has seen.
func newFixtureProxy(t *testing.T, dir string) (*httptest.Server, func() []string) {
	t.Helper()

	playlist, err := os.ReadFile(filepath.Join(dir, "playlist.m3u8"))
	require.NoError(t, err)

	var media []string
	seg := 0
	for _, line := range strings.Split(string(playlist), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			line = fmt.Sprintf("http://%s/%s", proxiedSegmentHosts[seg%len(proxiedSegmentHosts)], line)
			seg++
		}
		media = append(media, line)
	}
	require.GreaterOrEqual(t, seg, len(proxiedSegmentHosts), "the fixture must reach every segment host")

	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.String())
		mu.Unlock()

		switch name := path.Base(r.URL.Path); {
		case r.URL.Host == "" || r.Method == http.MethodConnect:
			http.Error(w, "not a proxy request", http.StatusBadRequest)
		case name == "master.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=128000\nhttp://variant.invalid/media.m3u8\n")
		case name == "media.m3u8":
			fmt.Fprint(w, strings.Join(media, "\n"))
		default:
			http.ServeFile(w, r, filepath.Join(dir, name))
		}
	}))
	t.Cleanup(srv.Close)

	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

func urlPullProxyParams(proxy string) *params.Params {
	return &params.Params{
		IngressInfo: &livekit.IngressInfo{InputType: livekit.IngressInput_URL_INPUT, Url: proxiedSourceURL},
		Config:      &config.Config{ServiceConfig: &config.ServiceConfig{URLPullProxy: proxy}},
	}
}

// pullThroughProxy runs the URL pull source into decodebin3, as Input does,
// until EOS or an error.
func pullThroughProxy(t *testing.T, proxy string) {
	t.Helper()

	gst.Init()

	src, err := urlpull.NewURLSource(context.Background(), urlPullProxyParams(proxy))
	require.NoError(t, err)

	pipeline, err := gstutil.ParseLaunch("decodebin3 name=decode ! fakesink sync=false")
	require.NoError(t, err)
	t.Cleanup(func() { _ = gstutil.SetStateBlocking(pipeline, gst.StateNull) })

	in := src.GetSources()[0]
	require.NoError(t, gstutil.Add(pipeline, in))
	require.NoError(t, gstutil.Link(in, pipeline.GetByName("decode")))

	require.NotEqual(t, gst.StateChangeFailure, pipeline.SetState(gst.StatePlaying))

	msg := pipeline.GetBus().TimedPopFiltered(
		gst.ClockTime(60*time.Second), gst.MessageEOS|gst.MessageError)
	require.NotNil(t, msg, "the pull neither finished nor failed")
	require.Equal(t, gst.MessageEOS, msg.Type(), msg.String())
}

// hlsdemux2 fetches playlists and segments with its own soup session, which
// takes its proxy from GLib's default resolver and so only from the process
// environment. The pull runs in a child process started with
// URLPullProxyEnv, as NewCmd starts a handler, because GLib picks the
// resolver once per process. XDG_CURRENT_DESKTOP=GNOME makes GLib prefer a
// resolver that ignores http_proxy, so the environment has to pin one.
//
// This guards against GStreamer, GLib or libproxy updates that change which
// requests reach the proxy.
func TestHLSPullFetchesThroughURLPullProxy(t *testing.T) {
	if os.Getenv(urlPullProxyChildEnv) == "1" {
		pullThroughProxy(t, os.Getenv(urlPullProxyAddrEnv))
		return
	}

	dir, _ := newHLSFixture(t, 4)

	run := func(t *testing.T, withEnv bool) ([]string, []byte, error) {
		proxy, seen := newFixtureProxy(t, dir)

		cmd := exec.Command(os.Args[0], "-test.run=^TestHLSPullFetchesThroughURLPullProxy$", "-test.v")
		cmd.Env = append(os.Environ(), "XDG_CURRENT_DESKTOP=GNOME",
			urlPullProxyChildEnv+"=1", urlPullProxyAddrEnv+"="+proxy.URL)
		if withEnv {
			cmd.Env = append(cmd.Env, urlPullProxyParams(proxy.URL).URLPullProxyEnv()...)
		}
		out, err := cmd.CombinedOutput()
		return seen(), out, err
	}

	t.Run("playlists and segments go through the proxy", func(t *testing.T) {
		seen, out, err := run(t, true)
		require.NoError(t, err, "the pull failed:\n%s", out)

		require.Contains(t, seen, proxiedSourceURL)
		require.Contains(t, seen, "http://variant.invalid/media.m3u8")
		for _, host := range proxiedSegmentHosts {
			require.True(t, containsHost(seen, host), "no segment from %s went through the proxy: %v", host, seen)
		}
	})

	// Without the environment only souphttpsrc's own fetch is proxied. If this
	// starts failing, hlsdemux2 takes its proxy from somewhere else and the
	// check above no longer shows that the environment is what routes it.
	t.Run("without the environment only the source fetch is proxied", func(t *testing.T) {
		seen, out, err := run(t, false)
		require.Error(t, err, "the pull cannot succeed without the proxy:\n%s", out)
		require.Equal(t, []string{proxiedSourceURL}, seen)
	})
}

func containsHost(urls []string, host string) bool {
	for _, u := range urls {
		if strings.HasPrefix(u, "http://"+host+"/") {
			return true
		}
	}
	return false
}
