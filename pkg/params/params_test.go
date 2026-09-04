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
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/testing/protocmp"
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
	require.Empty(t, cmp.Diff(expectedDefaultLayers, out.Layers, protocmp.Transform()))

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
	require.Empty(t, cmp.Diff(expected, out.Layers, protocmp.Transform()))
}

func newTestParams() *Params {
	return &Params{
		IngressInfo: &livekit.IngressInfo{State: &livekit.IngressState{}},
		logger:      logger.GetLogger(),
	}
}

// An ended session does not start again. Pads notify on their own GStreamer
// streaming threads and can fire after the input has gone away, and rolling
// the status back to a running one would leave the session looking live to
// anything tracking it by status.
func TestSetStatusIgnoresRunningStatusOnEndedSession(t *testing.T) {
	terminal := []livekit.IngressState_Status{
		livekit.IngressState_ENDPOINT_COMPLETE,
		livekit.IngressState_ENDPOINT_INACTIVE,
		livekit.IngressState_ENDPOINT_ERROR,
	}
	running := []livekit.IngressState_Status{
		livekit.IngressState_ENDPOINT_PUBLISHING,
		livekit.IngressState_ENDPOINT_BUFFERING,
	}

	for _, term := range terminal {
		for _, run := range running {
			t.Run(fmt.Sprintf("%s_then_%s", term, run), func(t *testing.T) {
				p := newTestParams()

				p.SetStatus(term, errors.New("room disconnected"))
				endedAt := p.State.EndedAt
				require.NotZero(t, endedAt)

				p.SetStatus(run, nil)

				require.Equal(t, term, p.State.Status)
				require.Equal(t, endedAt, p.State.EndedAt)
				require.EqualError(t, p.err, "room disconnected")
			})
		}
	}
}

// A finished session can still be enriched with further terminal state. The
// first end time and the first error stand.
func TestSetStatusAllowsTerminalUpdatesOnEndedSession(t *testing.T) {
	p := newTestParams()

	p.SetStatus(livekit.IngressState_ENDPOINT_ERROR, errors.New("room disconnected"))
	endedAt := p.State.EndedAt
	require.NotZero(t, endedAt)

	p.SetStatus(livekit.IngressState_ENDPOINT_COMPLETE, errors.New("later error"))

	require.Equal(t, livekit.IngressState_ENDPOINT_COMPLETE, p.State.Status)
	require.Equal(t, endedAt, p.State.EndedAt)
	require.EqualError(t, p.err, "room disconnected")
}

func TestSetStatusAllowsTransitionsBeforeTermination(t *testing.T) {
	p := newTestParams()

	p.SetStatus(livekit.IngressState_ENDPOINT_BUFFERING, nil)
	require.Equal(t, livekit.IngressState_ENDPOINT_BUFFERING, p.State.Status)
	require.Zero(t, p.State.EndedAt)

	p.SetStatus(livekit.IngressState_ENDPOINT_PUBLISHING, nil)
	require.Equal(t, livekit.IngressState_ENDPOINT_PUBLISHING, p.State.Status)
	require.Zero(t, p.State.EndedAt)

	p.SetStatus(livekit.IngressState_ENDPOINT_COMPLETE, nil)
	require.Equal(t, livekit.IngressState_ENDPOINT_COMPLETE, p.State.Status)
	require.NotZero(t, p.State.EndedAt)
}
