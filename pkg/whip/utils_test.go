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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractICEDetails(t *testing.T) {
	t.Run("trickle-ice-sdpfrag", func(t *testing.T) {
		// Modeled on the ICE restart request in RFC 9725, section 4.3.3
		frag := "a=ice-options:trickle ice2\r\n" +
			"a=group:BUNDLE 0 1\r\n" +
			"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\n" +
			"a=mid:0\r\n" +
			"a=ice-ufrag:ysXw\r\n" +
			"a=ice-pwd:vw5LmwG4y/e6dPP/zAP9Gp5k\r\n" +
			"a=candidate:1387637174 1 udp 2122260223 192.0.2.1 61764 typ host generation 0 ufrag EsAw network-id 1\r\n" +
			"a=end-of-candidates\r\n"

		ufrag, pwd, err := extractICEDetails([]byte(frag))
		require.NoError(t, err)
		require.Equal(t, "ysXw", ufrag)
		require.Equal(t, "vw5LmwG4y/e6dPP/zAP9Gp5k", pwd)
	})

	t.Run("session level sdpfrag", func(t *testing.T) {
		frag := "a=ice-ufrag:ysXw\r\na=ice-pwd:vw5LmwG4y/e6dPP/zAP9Gp5k\r\n"

		ufrag, pwd, err := extractICEDetails([]byte(frag))
		require.NoError(t, err)
		require.Equal(t, "ysXw", ufrag)
		require.Equal(t, "vw5LmwG4y/e6dPP/zAP9Gp5k", pwd)
	})

	t.Run("full session description", func(t *testing.T) {
		offer := "v=0\r\n" +
			"o=- 0 0 IN IP4 127.0.0.1\r\n" +
			"s=-\r\n" +
			"t=0 0\r\n" +
			"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\n" +
			"a=mid:0\r\n" +
			"a=ice-ufrag:ysXw\r\n" +
			"a=ice-pwd:vw5LmwG4y/e6dPP/zAP9Gp5k\r\n"

		ufrag, pwd, err := extractICEDetails([]byte(offer))
		require.NoError(t, err)
		require.Equal(t, "ysXw", ufrag)
		require.Equal(t, "vw5LmwG4y/e6dPP/zAP9Gp5k", pwd)
	})

	t.Run("invalid body", func(t *testing.T) {
		_, _, err := extractICEDetails([]byte("not an sdp fragment"))
		require.Error(t, err)
	})
}
