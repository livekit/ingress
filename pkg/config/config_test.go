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

package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/errors"
)

func TestInitDefaultsValidatesURLPullProxy(t *testing.T) {
	for _, tc := range []struct {
		proxy string
		valid bool
	}{
		{"", true},
		{"http://127.0.0.1:3128", true},
		{"http://proxy.local", true},
		{"http://127.0.0.1:99999", false},
		{"https://127.0.0.1:3128", false},
		{"127.0.0.1:3128", false},
		{"http://user:pass@127.0.0.1:3128", false},
	} {
		t.Run(tc.proxy, func(t *testing.T) {
			// A negative WHIP port skips the RTC config, which needs the network.
			err := (&ServiceConfig{WHIPPort: -1, URLPullProxy: tc.proxy}).InitDefaults()
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, errors.ErrInvalidURLPullProxy, "an unusable proxy must fail at startup")
			}
		})
	}
}
