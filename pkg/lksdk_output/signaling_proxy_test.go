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

package lksdk_output

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/params"
)

func TestSignalingBypassesURLPullProxy(t *testing.T) {
	var hits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(proxy.Close)

	// http.ProxyFromEnvironment reads the environment once per process, so set it before any dial.
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)

	for _, tc := range []struct {
		name     string
		input    livekit.IngressInput
		inputURL string
		proxied  bool
	}{
		{"rtmp signaling uses the environment proxy", livekit.IngressInput_RTMP_INPUT, "rtmp://example.com/x", true},
		{"http url pull signaling dials directly", livekit.IngressInput_URL_INPUT, "https://example.com/live.m3u8", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := http.DefaultTransport.(*http.Transport)
			origDialer, origTransport := websocket.DefaultDialer.Proxy, transport.Proxy
			t.Cleanup(func() {
				websocket.DefaultDialer.Proxy = origDialer
				transport.Proxy = origTransport
			})
			hits.Store(0)

			_, err := NewLKSDKOutput(context.Background(), nil, &params.Params{
				IngressInfo: &livekit.IngressInfo{InputType: tc.input, Url: tc.inputURL},
				Config:      &config.Config{ServiceConfig: &config.ServiceConfig{URLPullProxy: proxy.URL}},
				WsUrl:       "ws://signal.invalid",
				Token:       "token",
			})
			require.Error(t, err)

			if tc.proxied {
				require.NotZero(t, hits.Load(), "the test proxy must see signaling when no bypass applies")
			} else {
				require.Zero(t, hits.Load(), "signaling must not go through the URL pull proxy")
			}
		})
	}
}
