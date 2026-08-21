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
	"fmt"
	"testing"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/types"
)

const (
	testCapsSystemMemory = "video/x-raw, format=(string)NV12, width=(int)1280, height=(int)720, framerate=(fraction)30/1"
	testCapsGLMemory     = "video/x-raw(memory:GLMemory), format=(string)NV12, width=(int)1280, height=(int)720, framerate=(fraction)30/1, texture-target=(string)rectangle"
	testCapsSmaller      = "video/x-raw, format=(string)NV12, width=(int)640, height=(int)360, framerate=(fraction)30/1"
	testCapsAudio        = "audio/x-raw, format=(string)F32LE, rate=(int)48000, channels=(int)2"
)

// newTestVideoSinkPad builds a bin with the same shape as the video output bin --
// a ghost sink on videoconvert, a tee, and one layer branch ending in a
// capsfilter fixed to a single resolution -- and returns its sink pad.
func newTestVideoSinkPad(t *testing.T, width, height int) *gst.Pad {
	t.Helper()

	bin := gst.NewBin("test video output bin")
	t.Cleanup(func() {
		_ = bin.SetState(gst.StateNull)
	})

	convert, err := gst.NewElement("videoconvert")
	require.NoError(t, err)
	tee, err := gst.NewElement("tee")
	require.NoError(t, err)
	queue, err := gst.NewElement("queue")
	require.NoError(t, err)
	scale, err := gst.NewElement("videoscale")
	require.NoError(t, err)
	capsFilter, err := gst.NewElement("capsfilter")
	require.NoError(t, err)
	sink, err := gst.NewElement("fakesink")
	require.NoError(t, err)

	require.NoError(t, capsFilter.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("video/x-raw,width=%d,height=%d", width, height))))

	require.NoError(t, bin.AddMany(convert, tee, queue, scale, capsFilter, sink))
	require.NoError(t, gst.ElementLinkMany(convert, tee))
	require.NoError(t, gst.ElementLinkMany(queue, scale, capsFilter, sink))
	require.Equal(t, gst.PadLinkOK, tee.GetRequestPad("src_%u").Link(queue.GetStaticPad("sink")))

	ghost := gst.NewGhostPad("sink", convert.GetStaticPad("sink"))
	require.True(t, bin.AddPad(ghost.Pad))

	return bin.GetStaticPad("sink")
}

// The renegotiation this fix exists for: on GStreamer 1.28 the video pad is
// advertised as memory:GLMemory and then renegotiated to system memory. Both are
// acceptable to the established output, so the session continues.
func TestValidateCapsAllowsMemoryFeatureChange(t *testing.T) {
	gst.Init(nil)

	est := &establishedOutput{sinkPad: newTestVideoSinkPad(t, 1280, 720)}

	glMemory := gst.NewCapsFromString(testCapsGLMemory)
	require.NotNil(t, glMemory)
	require.NoError(t, est.validateCaps(types.Video, glMemory))

	systemMemory := gst.NewCapsFromString(testCapsSystemMemory)
	require.NotNil(t, systemMemory)
	require.NoError(t, est.validateCaps(types.Video, systemMemory))
}

// A resolution change is deliberately tolerated. videoscale rescales whatever
// arrives to each layer's fixed target, so data keeps flowing; the layers just
// keep their original geometry. Dropping a live session would be worse, and an
// adaptive source switching variants is behaving as designed.
func TestValidateCapsToleratesResolutionChange(t *testing.T) {
	gst.Init(nil)

	est := &establishedOutput{sinkPad: newTestVideoSinkPad(t, 1280, 720)}

	smaller := gst.NewCapsFromString(testCapsSmaller)
	require.NotNil(t, smaller)
	require.NoError(t, est.validateCaps(types.Video, smaller))
}

// Caps the established pad cannot accept at all are surfaced, since the pipeline
// would fail on them anyway.
func TestValidateCapsRejectsCapsThePadCannotAccept(t *testing.T) {
	gst.Init(nil)

	est := &establishedOutput{sinkPad: newTestVideoSinkPad(t, 1280, 720)}

	audio := gst.NewCapsFromString(testCapsAudio)
	require.NotNil(t, audio)

	err := est.validateCaps(types.Video, audio)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not accept renegotiated caps")
}

// With no pad recorded there is nothing to query, so nothing is rejected.
func TestValidateCapsWithoutSinkPad(t *testing.T) {
	gst.Init(nil)

	est := &establishedOutput{}

	caps := gst.NewCapsFromString(testCapsAudio)
	require.NotNil(t, caps)
	require.NoError(t, est.validateCaps(types.Audio, caps))
}
