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
	"testing"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/utils"
	"github.com/livekit/protocol/livekit"
)

// The encoder settings reach the elements. gobject panics on a Go int or uint
// property value, so building an output is enough to catch an unsized one.
func TestVideoOutputEncoderSettings(t *testing.T) {
	gst.Init()

	layer := &livekit.VideoLayer{Quality: livekit.VideoQuality_HIGH, Width: 1280, Height: 720, Bitrate: 2_000_000}

	for _, tc := range []struct {
		codec    livekit.VideoCodec
		property string
		want     any
	}{
		{livekit.VideoCodec_H264_BASELINE, "bitrate", uint32(2000)},
		{livekit.VideoCodec_VP8, "target-bitrate", int32(2_000_000)},
	} {
		t.Run(tc.codec.String(), func(t *testing.T) {
			o, err := NewVideoOutput(tc.codec, layer, utils.NewOutputSynchronizer().AddTrack(),
				func() bool { return false }, stats.NewLocalMediaStatsGatherer(), newEOSDispatcher())
			require.NoError(t, err)
			t.Cleanup(func() { _ = o.Close() })

			require.Equal(t, tc.want, o.enc.ObjectProperty(tc.property))
			require.Equal(t, uint32(1), o.bin.GetByName("video_HIGH_in").ObjectProperty("max-size-buffers"))
		})
	}
}

func TestAudioOutputEncoderSettings(t *testing.T) {
	gst.Init()

	o, err := NewAudioOutput(&livekit.IngressAudioEncodingOptions{
		AudioCodec: livekit.AudioCodec_OPUS,
		Bitrate:    64000,
		Channels:   2,
	}, utils.NewOutputSynchronizer().AddTrack(), func() bool { return false }, stats.NewLocalMediaStatsGatherer(), newEOSDispatcher())
	require.NoError(t, err)
	t.Cleanup(func() { _ = o.Close() })

	require.Equal(t, int32(64000), o.enc.ObjectProperty("bitrate"))
	require.Equal(t, uint64(5e7), o.bin.GetByName("audio_out").ObjectProperty("max-size-time"))
}
