package media

import (
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
)

func TestComputeVideoLayers(t *testing.T) {
	expectedLayers := []*livekit.VideoLayer{
		&livekit.VideoLayer{
			Quality: livekit.VideoQuality_HIGH,
			Width:   1280,
			Height:  720,
			Bitrate: 1700000,
		},
		&livekit.VideoLayer{
			Quality: livekit.VideoQuality_MEDIUM,
			Width:   640,
			Height:  360,
			Bitrate: 601040,
		},
		&livekit.VideoLayer{
			Quality: livekit.VideoQuality_LOW,
			Width:   320,
			Height:  180,
			Bitrate: 212499,
		},
	}

	l := computeVideoLayers(expectedLayers[0], 3)
	require.Equal(t, expectedLayers, l)

	expectedLayers[1].Quality = livekit.VideoQuality_LOW
	l = computeVideoLayers(expectedLayers[0], 2)
	require.Equal(t, expectedLayers[:2], l)

	l = computeVideoLayers(expectedLayers[0], 1)
	require.Equal(t, expectedLayers[:1], l)

}
