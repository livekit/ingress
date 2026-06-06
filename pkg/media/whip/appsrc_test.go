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

package whip

import (
	"testing"

	"github.com/go-gst/go-gst/gst"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

func TestGetCapsForCodecH264UsesAccessUnitAlignment(t *testing.T) {
	gst.Init(nil)

	caps, err := getCapsForCodec(webrtc.MimeTypeH264)
	require.NoError(t, err)
	require.NotNil(t, caps)

	structure := caps.GetStructureAt(0)
	require.NotNil(t, structure)
	require.Equal(t, "video/x-h264", structure.Name())

	streamFormat, err := structure.GetValue("stream-format")
	require.NoError(t, err)
	require.Equal(t, "byte-stream", streamFormat)

	alignment, err := structure.GetValue("alignment")
	require.NoError(t, err)
	require.Equal(t, "au", alignment)
}
