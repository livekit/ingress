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

package media

import (
	"testing"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/types"
)

const testNegotiatedCaps = "video/x-raw,format=NV12,width=1280,height=720,framerate=30/1"

// newNegotiatedGhostPad returns a ghost pad shaped like the one Input surfaces:
// a bin sink pad in the data path, so the pad carries real negotiated caps and
// its "caps" property is set the way onParamsReady expects.
func newNegotiatedGhostPad(t *testing.T, capsStr string) *gst.GhostPad {
	t.Helper()

	pipeline, err := gst.NewPipeline("test negotiation pipeline")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = pipeline.SetState(gst.StateNull)
	})

	src, err := gst.NewElement("videotestsrc")
	require.NoError(t, err)
	capsFilter, err := gst.NewElement("capsfilter")
	require.NoError(t, err)
	require.NoError(t, capsFilter.SetProperty("caps", gst.NewCapsFromString(capsStr)))
	fakeSink, err := gst.NewElement("fakesink")
	require.NoError(t, err)

	bin := gst.NewBin("test output bin")
	require.NoError(t, bin.Add(fakeSink))
	ghost := gst.NewGhostPad("video", fakeSink.GetStaticPad("sink"))
	require.True(t, bin.AddPad(ghost.Pad))

	require.NoError(t, pipeline.AddMany(src, capsFilter, bin.Element))
	require.NoError(t, gst.ElementLinkMany(src, capsFilter, bin.Element))

	require.NoError(t, pipeline.SetState(gst.StatePaused))
	ret, _ := pipeline.GetState(gst.StatePaused, gst.ClockTimeNone)
	require.NotEqual(t, gst.StateChangeFailure, ret)
	require.NotNil(t, ghost.GetCurrentCaps(), "ghost pad did not negotiate")

	return ghost
}

// The renegotiation this fix exists for. On GStreamer 1.28 the video pad is
// advertised as memory:GLMemory and then renegotiated to system memory, so
// onParamsReady runs twice for one pad. The second run must not build another
// output: the bin name is hardcoded, so gst_bin_add would reject it.
//
// sink is deliberately left nil. Building an output dereferences it, so a
// regression here fails loudly instead of silently rebuilding.
func TestSecondCapsNotificationDoesNotRebuild(t *testing.T) {
	gst.Init(nil)

	const builtCaps = "video/x-raw(memory:GLMemory),format=NV12,width=1280,height=720"

	p := &Pipeline{
		established: map[types.StreamKind]string{types.Video: builtCaps},
	}

	p.onParamsReady(types.Video, newNegotiatedGhostPad(t, testNegotiatedCaps))

	require.Equal(t, builtCaps, p.established[types.Video],
		"the established caps must survive a renegotiation")
	require.Len(t, p.established, 1)
}

// A notification carrying no caps is not a renegotiation and must not record
// anything, or the real caps that follow would be treated as the second one.
func TestCapsNotificationWithoutCapsIsIgnored(t *testing.T) {
	gst.Init(nil)

	p := &Pipeline{
		established: make(map[types.StreamKind]string),
	}

	p.onParamsReady(types.Video, gst.NewGhostPadNoTarget("video", gst.PadDirectionSink))

	require.Empty(t, p.established)
}
