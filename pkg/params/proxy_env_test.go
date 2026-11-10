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

package params

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http/httpproxy"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/ingress/pkg/config"
)

const testProxy = "http://127.0.0.1:3128"

func newProxyTestParams(input livekit.IngressInput, inputURL, proxy string) *Params {
	return &Params{
		IngressInfo: &livekit.IngressInfo{InputType: input, Url: inputURL},
		Config:      &config.Config{ServiceConfig: &config.ServiceConfig{URLPullProxy: proxy}},
		WsUrl:       "wss://project.livekit.cloud",
	}
}

func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func TestURLPullProxyEnv(t *testing.T) {
	t.Run("http url pull is proxied except for signaling", func(t *testing.T) {
		env := envMap(newProxyTestParams(livekit.IngressInput_URL_INPUT, "https://example.com/live.m3u8", testProxy).URLPullProxyEnv())

		// libproxy prefers the lowercase names and Go the uppercase ones; both must agree.
		for _, k := range []string{"http_proxy", "https_proxy", "no_proxy"} {
			require.Equal(t, env[k], env[strings.ToUpper(k)], k)
		}

		resolve := (&httpproxy.Config{
			HTTPProxy:  env["HTTP_PROXY"],
			HTTPSProxy: env["HTTPS_PROXY"],
			NoProxy:    env["NO_PROXY"],
		}).ProxyFunc()

		for _, target := range []string{
			"http://169.254.169.254/latest/meta-data/",
			"http://10.0.0.1:8080/",
			"https://example.com/seg0.ts",
		} {
			u, err := url.Parse(target)
			require.NoError(t, err)
			got, err := resolve(u)
			require.NoError(t, err)
			require.NotNil(t, got, "%s must go through the proxy", target)
			require.Equal(t, testProxy, got.String())
		}

		u, err := url.Parse("https://project.livekit.cloud/rtc")
		require.NoError(t, err)
		got, err := resolve(u)
		require.NoError(t, err)
		require.Nil(t, got, "signaling must not go through the proxy")
	})

	for _, tc := range []struct {
		name     string
		input    livekit.IngressInput
		inputURL string
		proxy    string
	}{
		{"no proxy configured", livekit.IngressInput_URL_INPUT, "https://example.com/live.m3u8", ""},
		{"srt url pull", livekit.IngressInput_URL_INPUT, "srt://example.com:9000", testProxy},
		{"rtmp", livekit.IngressInput_RTMP_INPUT, "rtmp://example.com/x", testProxy},
		{"whip", livekit.IngressInput_WHIP_INPUT, "https://example.com/w", testProxy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Nil(t, newProxyTestParams(tc.input, tc.inputURL, tc.proxy).URLPullProxyEnv())
		})
	}
}
