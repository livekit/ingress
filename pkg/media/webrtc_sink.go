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
	"context"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/ingress/pkg/lksdk_output"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	"github.com/livekit/protocol/utils"
)

type WebRTCSink struct {
	params *params.Params

	sdkOut *lksdk_output.LKSDKOutput
}

func NewWebRTCSink(ctx context.Context, p *params.Params) (*WebRTCSink, error) {
	ctx, span := tracer.Start(ctx, "media.NewWebRTCSink")
	defer span.End()

	sdkOut, err := lksdk_output.NewLKSDKOutput(ctx, p)
	if err != nil {
		return nil, err
	}

	return &WebRTCSink{
		params: p,
		sdkOut: sdkOut,
	}, nil
}

func (s *WebRTCSink) addAudioTrack() (*Output, error) {
	output, err := NewAudioOutput(s.params.AudioEncodingOptions)
	if err != nil {
		logger.Errorw("could not create output", err)
		return nil, err
	}

	err = s.sdkOut.AddAudioTrack(output, utils.GetMimeTypeForAudioCodec(s.params.AudioEncodingOptions.AudioCodec), s.params.AudioEncodingOptions.DisableDtx, s.params.AudioEncodingOptions.Channels > 1)
	if err != nil {
		return nil, err
	}

	return output.Output, nil
}

func (s *WebRTCSink) addVideoTrack() ([]*Output, error) {
	outputs := make([]*Output, 0)
	sbArray := make([]lksdk_output.VideoSampleProvider, 0)
	for _, layer := range s.params.VideoEncodingOptions.Layers {
		output, err := NewVideoOutput(s.params.VideoEncodingOptions.VideoCodec, layer)
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, output.Output)
		sbArray = append(sbArray, output)
	}

	err := s.sdkOut.AddVideoTrack(sbArray, s.params.VideoEncodingOptions.Layers, utils.GetMimeTypeForVideoCodec(s.params.VideoEncodingOptions.VideoCodec))
	if err != nil {
		return nil, err
	}

	return outputs, nil
}

func (s *WebRTCSink) AddTrack(kind types.StreamKind) (*gst.Bin, error) {
	var bin *gst.Bin

	switch kind {
	case types.Audio:
		output, err := s.addAudioTrack()
		if err != nil {
			logger.Errorw("could not add audio track", err)
			return nil, err
		}

		bin = output.bin

	case types.Video:
		outputs, err := s.addVideoTrack()
		if err != nil {
			logger.Errorw("could not add video track", err)
			return nil, err
		}

		pp, err := NewVideoOutputBin(s.params.VideoEncodingOptions, outputs)
		if err != nil {
			logger.Errorw("could not create video output bin", err)
			return nil, err
		}

		bin = pp.GetBin()
	}

	return bin, nil
}

func (s *WebRTCSink) Close() {
	s.sdkOut.Close()
}
