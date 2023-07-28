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

func TestPopulateAudioEncodingOptionsDefaults(t *testing.T) {
	in := &livekit.IngressAudioEncodingOptions{}

	out, err := populateAudioEncodingOptionsDefaults(in)
	require.NoError(t, err)
	require.Equal(t, livekit.AudioCodec_OPUS, out.AudioCodec)
	require.Equal(t, uint32(2), out.Channels)
	require.Equal(t, uint32(96000), out.Bitrate)

	in.Channels = 1
	out, err = populateAudioEncodingOptionsDefaults(in)
	require.NoError(t, err)
	require.Equal(t, livekit.AudioCodec_OPUS, out.AudioCodec)
	require.Equal(t, uint32(1), out.Channels)
	require.Equal(t, uint32(64000), out.Bitrate)
}

func TestPopulateVideoEncodingOptionsDefaults(t *testing.T) {
	in := &livekit.IngressVideoEncodingOptions{}

	out, err := populateVideoEncodingOptionsDefaults(in)
	require.NoError(t, err)
	require.Equal(t, livekit.VideoCodec_H264_BASELINE, out.VideoCodec)
	require.Equal(t, float64(30), out.FrameRate)
	require.Equal(t, expectedDefaultLayers, out.Layers)

	in.FrameRate = 15
	in.Layers = []*livekit.VideoLayer{
		&livekit.VideoLayer{
			Width:   1920,
			Height:  1080,
			Quality: livekit.VideoQuality_HIGH,
		},
		&livekit.VideoLayer{
			Width:   480,
			Height:  270,
			Quality: livekit.VideoQuality_LOW,
		},
	}
	expected := []*livekit.VideoLayer{
		&livekit.VideoLayer{
			Width:   1920,
			Height:  1080,
			Bitrate: 2_081_112,
			Quality: livekit.VideoQuality_HIGH,
		},
		&livekit.VideoLayer{
			Width:   480,
			Height:  270,
			Bitrate: 260_139,
			Quality: livekit.VideoQuality_LOW,
		},
	}

	out, err = populateVideoEncodingOptionsDefaults(in)
	require.NoError(t, err)
	require.Equal(t, livekit.VideoCodec_H264_BASELINE, out.VideoCodec)
	require.Equal(t, float64(15), out.FrameRate)
	require.Equal(t, expected, out.Layers)
}
