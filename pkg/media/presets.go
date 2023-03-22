package media

import (
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/livekit"
)

func getOptionsForVideoPreset(preset livekit.IngressVideoEncodingPreset) (*livekit.IngressVideoEncodingOptions, error) {
	switch preset {
	case livekit.IngressVideoEncodingPreset_H264_720P_30FPS_3_LAYERS:
		return &livekit.IngressVideoEncodingOptions{
			VideoCodec: livekit.VideoCodec_H264_BASELINE,
			FrameRate:  30,
			Layers: computeVideoLayers(&livekit.VideoLayer{
				Quality: livekit.VideoQuality_HIGH,
				Width:   1280,
				Height:  720,
				Bitrate: 1700000,
			}, 3),
		}, nil
	default:
		return nil, errors.ErrInvalidVideoPreset
	}
}

func computeVideoLayers(highLayer *livekit.VideoLayer, layerCount int) []*livekit.VideoLayer {
	layerCopy := *highLayer
	layerCopy.Quality = livekit.VideoQuality_HIGH

	layers := []*livekit.VideoLayer{
		&layerCopy,
	}

	for i := 1; i < layerCount; i++ {
		layer := &livekit.VideoLayer{
			Width:   layerCopy.Width >> i, // each layer has dimentions half of the previous one
			Height:  layerCopy.Height >> i,
			Bitrate: layerCopy.Bitrate >> (2 * i), // bitrate 1/4 of the previous layer
			Quality: livekit.VideoQuality(layerCount - 1 - i),
		}

		layers = append(layers, layer)
	}

	return layers
}

func getOptionsForAudioPreset(preset livekit.IngressAudioEncodingPreset) (*livekit.IngressAudioEncodingOptions, error) {
	switch preset {
	case livekit.IngressAudioEncodingPreset_OPUS_STEREO_96KBPS:
		return &livekit.IngressAudioEncodingOptions{
			AudioCodec: livekit.AudioCodec_OPUS,
			Channels:   2,
			Bitrate:    96000,
		}, nil
	default:
		return nil, errors.ErrInvalidAudioPreset
	}
}
