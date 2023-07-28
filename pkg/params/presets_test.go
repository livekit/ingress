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

package params

import (
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
)

var (
	expectedDefaultLayers = []*livekit.VideoLayer{
		&livekit.VideoLayer{
			Quality: livekit.VideoQuality_HIGH,
			Width:   1280,
			Height:  720,
			Bitrate: 1700000,
		},
		&livekit.VideoLayer{
			Quality: livekit.VideoQuality_MEDIUM,
			Width:   640,
			Height:  360,
			Bitrate: 601040,
		},
		&livekit.VideoLayer{
			Quality: livekit.VideoQuality_LOW,
			Width:   320,
			Height:  180,
			Bitrate: 212500,
		},
	}
)

func TestComputeVideoLayers(t *testing.T) {

	l := computeVideoLayers(expectedDefaultLayers[0], 3)
	require.Equal(t, expectedDefaultLayers, l)

	expectedDefaultLayers[1].Quality = livekit.VideoQuality_LOW
	l = computeVideoLayers(expectedDefaultLayers[0], 2)
	require.Equal(t, expectedDefaultLayers[:2], l)

	l = computeVideoLayers(expectedDefaultLayers[0], 1)
	require.Equal(t, expectedDefaultLayers[:1], l)

}
