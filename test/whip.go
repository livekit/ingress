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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

const whipClientPath = "livekit-whip-bot/cmd/whip-client/whip-client"

// RunWHIPTest publishes over WHIP with transcoding enabled and stops the
// ingress once it has been streaming for a while, which is a stop we asked for
// and must be reported as a clean end.
func RunWHIPTest(t *testing.T, r *Runner) {
	transcode := true

	info := r.ingressInfo(livekit.IngressInput_WHIP_INPUT, "ingress-test-whip")
	info.Url = fmt.Sprintf("http://localhost:%d/w", r.WHIPPort)
	info.EnableTranscoding = &transcode
	withVideo(info,
		videoLayer(livekit.VideoQuality_HIGH, 1280, 720, 3000000),
		videoLayer(livekit.VideoQuality_LOW, 640, 360, 1000000),
	)

	r.registerIngress(t, info)

	whipURL := fmt.Sprintf("%s/%s", info.Url, info.StreamKey)
	logger.Infow("whip url", "url", whipURL)

	publish(t, fmt.Sprintf("%s -url %s", whipClientPath, whipURL))

	r.checkUpdate(t, info.IngressId, livekit.IngressState_ENDPOINT_PUBLISHING)
	time.Sleep(streamDuration)

	state := r.stopIngress(t, info.IngressId)
	require.Equal(t, livekit.IngressState_ENDPOINT_INACTIVE, state.Status)

	r.awaitIdle(t)
}
