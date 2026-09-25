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
	"testing"

	"github.com/livekit/protocol/livekit"
)

const hlsPullURL = "http://devimages.apple.com/iphone/samples/bipbop/gear4/prog_index.m3u8"

// hlsPull points the ingress at a public HLS source that outlasts any case.
type hlsPull struct{}

func (*hlsPull) pulls() bool                              { return true }
func (*hlsPull) url(*testing.T, *Runner, string) string   { return hlsPullURL }
func (*hlsPull) publish(*testing.T, *livekit.IngressInfo) {}
func (*hlsPull) endStream(*testing.T)                     {}

func (r *Runner) testURL(t *testing.T) {
	if !r.runURL() {
		return
	}

	t.Run("URL", func(t *testing.T) {
		for _, tc := range []*testCase{
			{
				name:      "HLS/DeletedWhilePublishing",
				inputType: livekit.IngressInput_URL_INPUT,
				source:    &hlsPull{},
				video:     videoOptions(videoLayer(livekit.VideoQuality_HIGH, 1280, 720, 3000000)),
				reusable:  true,
				endBy:     endByDelete,
				runFor:    streamDuration,
				expect:    livekit.IngressState_ENDPOINT_INACTIVE,
			},
			{
				name:      "HLS/OriginFailsMidStream",
				inputType: livekit.IngressInput_URL_INPUT,
				source:    &truncatedHLS{segments: truncatedFixtureSegments, good: truncatedGoodSegments},
				// A source that ran to its end is reported as COMPLETE, which
				// is the status the error has to be distinguishable from.
				reusable:       false,
				skipPublishing: true,
				endBy:          endBySource,
				expect:         livekit.IngressState_ENDPOINT_ERROR,
				errorLike:      "source ended after",
			},
		} {
			r.run(t, tc)
		}
	})
}
