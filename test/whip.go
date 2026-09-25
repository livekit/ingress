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
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
)

// whipClientBinary is the publisher from the livekit-whip-bot submodule. It is
// looked up on PATH rather than at a path relative to the working directory,
// which is /workspace under the prebuilt test binary and /workspace/test under
// go test.
const whipClientBinary = "whip-client"

// whipPublisher publishes a synthetic camera and microphone over WHIP for the
// life of the case.
type whipPublisher struct {
	proc *publisher
}

func (*whipPublisher) pulls() bool { return false }

// url is the endpoint prefix. The stream key is appended per publisher rather
// than baked in here, since it is also what the ingress is resolved by.
func (*whipPublisher) url(_ *testing.T, r *Runner, _ string) string {
	return fmt.Sprintf("http://localhost:%d/w", r.WHIPPort)
}

func (s *whipPublisher) publish(t *testing.T, info *livekit.IngressInfo) {
	bin, err := exec.LookPath(whipClientBinary)
	require.NoError(t, err, "build it with: go build -o <dir on PATH>/%s "+
		"./cmd/whip-client, from test/livekit-whip-bot", whipClientBinary)

	s.proc = publish(t, fmt.Sprintf("%s -url %s/%s", bin, info.Url, info.StreamKey))
}

// endStream signals whip-client, which sends the WHIP DELETE for its session
// before exiting.
func (s *whipPublisher) endStream(t *testing.T) { s.proc.endStream(t) }

func (r *Runner) testWHIP(t *testing.T) {
	if !r.runWHIP() {
		return
	}

	transcode := true

	t.Run("WHIP", func(t *testing.T) {
		for _, tc := range []*testCase{
			{
				name:      "DeletedWhilePublishing",
				inputType: livekit.IngressInput_WHIP_INPUT,
				source:    &whipPublisher{},
				video: videoOptions(
					videoLayer(livekit.VideoQuality_HIGH, 1280, 720, 3000000),
					videoLayer(livekit.VideoQuality_LOW, 640, 360, 1000000),
				),
				enableTranscoding: &transcode,
				reusable:          true,
				endBy:             endByDelete,
				runFor:            streamDuration,
				// Unlike RTMP, the WHIP read is not bound to the context the
				// stop cancels, so the session ends clean.
				expect: livekit.IngressState_ENDPOINT_INACTIVE,
			},
			{
				name:      "EndedByPublisher",
				inputType: livekit.IngressInput_WHIP_INPUT,
				source:    &whipPublisher{},
				video: videoOptions(
					videoLayer(livekit.VideoQuality_HIGH, 1280, 720, 3000000),
					videoLayer(livekit.VideoQuality_LOW, 640, 360, 1000000),
				),
				enableTranscoding: &transcode,
				reusable:          false,
				endBy:             endBySource,
				runFor:            sourceEndDuration,
				expect:            livekit.IngressState_ENDPOINT_COMPLETE,
			},
		} {
			r.run(t, tc)
		}
	})
}
