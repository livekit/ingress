package media

import (
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
)

var (
	expectedDefaultLayers = []*livekit.VideoLayer{
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
			Bitrate: 212500,
		},
	}
)

func TestComputeVideoLayers(t *testing.T) {

	l := computeVideoLayers(expectedDefaultLayers[0], 3)
	require.Equal(t, expectedDefaultLayers, l)

	expectedDefaultLayers[1].Quality = livekit.VideoQuality_LOW
	l = computeVideoLayers(expectedDefaultLayers[0], 2)
	require.Equal(t, expectedDefaultLayers[:2], l)

	l = computeVideoLayers(expectedDefaultLayers[0], 1)
	require.Equal(t, expectedDefaultLayers[:1], l)

}
