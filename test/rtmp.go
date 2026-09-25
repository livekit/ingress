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

//go:build integration

package test

import (
	"fmt"
	"testing"

	"github.com/livekit/protocol/livekit"
)

// rtmpPublisher pushes a test pattern over RTMP for the life of the case.
type rtmpPublisher struct{}

func (*rtmpPublisher) pulls() bool { return false }

func (*rtmpPublisher) url(_ *testing.T, r *Runner, streamKey string) string {
	return fmt.Sprintf("rtmp://localhost:%d/live/%s", r.RTMPPort, streamKey)
}

func (*rtmpPublisher) publish(t *testing.T, info *livekit.IngressInfo) {
	publish(t, fmt.Sprintf(
		"gst-launch-1.0 -v flvmux name=mux ! rtmp2sink location=%s "+
			"audiotestsrc freq=200 ! faac ! mux. "+
			"videotestsrc pattern=ball is-live=true ! video/x-raw,width=1280,height=720 ! x264enc speed-preset=3 tune=zerolatency ! mux.",
		info.Url))
}

func (r *Runner) testRTMP(t *testing.T) {
	if !r.runRTMP() {
		return
	}

	t.Run("RTMP", func(t *testing.T) {
		for _, tc := range []*testCase{
			{
				name:      "DeletedWhilePublishing",
				inputType: livekit.IngressInput_RTMP_INPUT,
				source:    &rtmpPublisher{},
				video:     videoOptions(videoLayer(livekit.VideoQuality_HIGH, 1280, 720, 3000000)),
				reusable:  true,
				endBy:     endByDelete,
				runFor:    streamDuration,
				// The stop lands while the source is still streaming, which
				// cancels its relay read mid-read. The session ends on that
				// cancellation.
				expect: livekit.IngressState_ENDPOINT_ERROR,
			},
		} {
			r.run(t, tc)
		}
	})
}
