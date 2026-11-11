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

package urlpull

import (
	"context"
	"testing"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/params"
)

func init() {
	gst.Init()
}

func newURLPullParams(inputURL, proxy string) *params.Params {
	return &params.Params{
		IngressInfo: &livekit.IngressInfo{InputType: livekit.IngressInput_URL_INPUT, Url: inputURL},
		Config:      &config.Config{ServiceConfig: &config.ServiceConfig{URLPullProxy: proxy}},
	}
}

func TestNewURLSourceRejectsInvalidProxy(t *testing.T) {
	for _, proxy := range []string{
		"http://",
		"http://:3128",
		"http://127.0.0.1:0",
		"http://127.0.0.1:99999",
		"://127.0.0.1:3128",
		"http://[::1",
		"127.0.0.1:3128",
		"socks5://127.0.0.1:1080",
	} {
		t.Run(proxy, func(t *testing.T) {
			_, err := NewURLSource(context.Background(), newURLPullParams("https://example.com/live.m3u8", proxy))
			require.ErrorIs(t, err, errors.ErrInvalidURLPullProxy, "an unusable proxy must fail the ingress, not fetch directly")
		})
	}
}

func TestNewURLSourceAcceptsValidProxy(t *testing.T) {
	for _, proxy := range []string{"", "http://127.0.0.1:3128", "http://127.0.0.1:65535", "http://[::1]:3128", "http://proxy.local"} {
		t.Run(proxy, func(t *testing.T) {
			_, err := NewURLSource(context.Background(), newURLPullParams("https://example.com/live.m3u8", proxy))
			require.NoError(t, err)
		})
	}
}
