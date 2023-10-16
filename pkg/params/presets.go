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

package params

import (
	"math"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/livekit"
)

const (
	// reference parameters used to compute bitrate
	refBitrate   = 3_500_000
	refWidth     = 1920
	refHeight    = 1080
	refFramerate = 30
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
				Bitrate: 1_900_000,
			}, 3),
		}, nil
	case livekit.IngressVideoEncodingPreset_H264_1080P_30FPS_3_LAYERS:
		return &livekit.IngressVideoEncodingOptions{
			VideoCodec: livekit.VideoCodec_H264_BASELINE,
			FrameRate:  30,
			Layers: computeVideoLayers(&livekit.VideoLayer{
				Quality: livekit.VideoQuality_HIGH,
				Width:   1920,
				Height:  1080,
				Bitrate: 3_500_000,
			}, 3),
		}, nil
	case livekit.IngressVideoEncodingPreset_H264_540P_25FPS_2_LAYERS:
		return &livekit.IngressVideoEncodingOptions{
			VideoCodec: livekit.VideoCodec_H264_BASELINE,
			FrameRate:  25,
			Layers: computeVideoLayers(&livekit.VideoLayer{
				Quality: livekit.VideoQuality_HIGH,
				Width:   960,
				Height:  540,
				Bitrate: 1_000_000,
			}, 2),
		}, nil
	case livekit.IngressVideoEncodingPreset_H264_720P_30FPS_1_LAYER:
		return &livekit.IngressVideoEncodingOptions{
			VideoCodec: livekit.VideoCodec_H264_BASELINE,
			FrameRate:  30,
			Layers: computeVideoLayers(&livekit.VideoLayer{
				Quality: livekit.VideoQuality_HIGH,
				Width:   1280,
				Height:  720,
				Bitrate: 1_900_000,
			}, 1),
		}, nil
	case livekit.IngressVideoEncodingPreset_H264_1080P_30FPS_1_LAYER:
		return &livekit.IngressVideoEncodingOptions{
			VideoCodec: livekit.VideoCodec_H264_BASELINE,
			FrameRate:  30,
			Layers: computeVideoLayers(&livekit.VideoLayer{
				Quality: livekit.VideoQuality_HIGH,
				Width:   1920,
				Height:  1080,
				Bitrate: 3_500_000,
			}, 1),
		}, nil
	case livekit.IngressVideoEncodingPreset_H264_720P_30FPS_3_LAYERS_HIGH_MOTION:
		return &livekit.IngressVideoEncodingOptions{
			VideoCodec: livekit.VideoCodec_H264_BASELINE,
			FrameRate:  30,
			Layers: computeVideoLayers(&livekit.VideoLayer{
				Quality: livekit.VideoQuality_HIGH,
				Width:   1280,
				Height:  720,
				Bitrate: 2_500_000,
			}, 3),
		}, nil
	case livekit.IngressVideoEncodingPreset_H264_1080P_30FPS_3_LAYERS_HIGH_MOTION:
		return &livekit.IngressVideoEncodingOptions{
			VideoCodec: livekit.VideoCodec_H264_BASELINE,
			FrameRate:  30,
			Layers: computeVideoLayers(&livekit.VideoLayer{
				Quality: livekit.VideoQuality_HIGH,
				Width:   1920,
				Height:  1080,
				Bitrate: 4_500_000,
			}, 3),
		}, nil
	case livekit.IngressVideoEncodingPreset_H264_540P_25FPS_2_LAYERS_HIGH_MOTION:
		return &livekit.IngressVideoEncodingOptions{
			VideoCodec: livekit.VideoCodec_H264_BASELINE,
			FrameRate:  25,
			Layers: computeVideoLayers(&livekit.VideoLayer{
				Quality: livekit.VideoQuality_HIGH,
				Width:   960,
				Height:  540,
				Bitrate: 1_300_000,
			}, 2),
		}, nil
	case livekit.IngressVideoEncodingPreset_H264_720P_30FPS_1_LAYER_HIGH_MOTION:
		return &livekit.IngressVideoEncodingOptions{
			VideoCodec: livekit.VideoCodec_H264_BASELINE,
			FrameRate:  30,
			Layers: computeVideoLayers(&livekit.VideoLayer{
				Quality: livekit.VideoQuality_HIGH,
				Width:   1280,
				Height:  720,
				Bitrate: 2_500_000,
			}, 1),
		}, nil
	case livekit.IngressVideoEncodingPreset_H264_1080P_30FPS_1_LAYER_HIGH_MOTION:
		return &livekit.IngressVideoEncodingOptions{
			VideoCodec: livekit.VideoCodec_H264_BASELINE,
			FrameRate:  30,
			Layers: computeVideoLayers(&livekit.VideoLayer{
				Quality: livekit.VideoQuality_HIGH,
				Width:   1920,
				Height:  1080,
				Bitrate: 4_500_000,
			}, 1),
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
		w := layerCopy.Width >> i // each layer has dimentions half of the previous one
		h := layerCopy.Height >> i

		rate := getBitrateForParams(layerCopy.Bitrate, layerCopy.Width, layerCopy.Height, 1, w, h, 1)

		layer := &livekit.VideoLayer{
			Width:   w,
			Height:  h,
			Bitrate: rate,
			Quality: livekit.VideoQuality(layerCount - 1 - i),
		}

		layers = append(layers, layer)
	}

	return layers
}

func getBitrateForParams(refBitrate, refWidth, refHeight uint32, refFramerate float64, targetWidth, targetHeight uint32, targetFramerate float64) uint32 {
	// bitrate = ref_bitrate * (target framerate / ref framerate) ^ 0.75 * (target pixel area / ref pixel area) ^ 0.75

	ratio := math.Pow(float64(targetFramerate)/float64(refFramerate), 0.75)
	ratio = ratio * math.Pow(float64(targetWidth)*float64(targetHeight)/(float64(refWidth)*float64(refHeight)), 0.75)

	return uint32(float64(refBitrate) * ratio)
}

func getOptionsForAudioPreset(preset livekit.IngressAudioEncodingPreset) (*livekit.IngressAudioEncodingOptions, error) {
	switch preset {
	case livekit.IngressAudioEncodingPreset_OPUS_STEREO_96KBPS:
		return &livekit.IngressAudioEncodingOptions{
			AudioCodec: livekit.AudioCodec_OPUS,
			Channels:   2,
			Bitrate:    96000,
		}, nil
	case livekit.IngressAudioEncodingPreset_OPUS_MONO_64KBS:
		return &livekit.IngressAudioEncodingOptions{
			AudioCodec: livekit.AudioCodec_OPUS,
			Channels:   1,
			Bitrate:    64000,
		}, nil
	default:
		return nil, errors.ErrInvalidAudioPreset
	}
}
