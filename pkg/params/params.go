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
	"context"
	"fmt"
	"os"
	"path"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/ingress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
)

type Params struct {
	stateLock   sync.Mutex
	psrpcClient rpc.IOInfoClient

	*livekit.IngressInfo
	*config.Config

	AudioEncodingOptions *livekit.IngressAudioEncodingOptions
	VideoEncodingOptions *livekit.IngressVideoEncodingOptions

	// connection info
	WsUrl string
	Token string

	// relay info
	RelayUrl string
	TmpDir   string

	// Input type specific private parameters
	ExtraParams any
}

type WhipExtraParams struct {
	MimeTypes map[types.StreamKind]string `json:"mime_types"`
}

func GetParams(ctx context.Context, psrpcClient rpc.IOInfoClient, conf *config.Config, info *livekit.IngressInfo, wsUrl, token string, ep any) (*Params, error) {
	var err error

	// The state should have been created by the service, before launching the hander, but be defensive here.
	if info.State == nil || info.State.ResourceId == "" {
		return nil, errors.ErrMissingResourceId
	}

	relayUrl := ""
	fields := []interface{}{"ingressID", info.IngressId}
	switch info.InputType {
	case livekit.IngressInput_RTMP_INPUT:
		fields = append(fields, "resourceID", info.State.ResourceId)
		relayUrl = getRTMPRelayUrl(conf, info.State.ResourceId)
	case livekit.IngressInput_WHIP_INPUT:
		fields = append(fields, "resourceID", info.State.ResourceId)
		relayUrl = getWHIPRelayUrlPrefix(conf, info.State.ResourceId)
	}

	tmpDir := path.Join(os.TempDir(), info.State.ResourceId)

	err = conf.InitLogger(fields...)
	if err != nil {
		return nil, err
	}

	err = ingress.Validate(info)
	if err != nil {
		return nil, err
	}

	infoCopy := proto.Clone(info).(*livekit.IngressInfo)

	infoCopy.State.Status = livekit.IngressState_ENDPOINT_BUFFERING
	if infoCopy.State.StartedAt == 0 {
		infoCopy.State.StartedAt = time.Now().UnixNano()
	}

	if infoCopy.Audio == nil {
		infoCopy.Audio = &livekit.IngressAudioOptions{}
	}

	if infoCopy.Video == nil {
		infoCopy.Video = &livekit.IngressVideoOptions{}
	}

	audioEncodingOptions, err := getAudioEncodingOptions(infoCopy.Audio)
	if err != nil {
		return nil, err
	}

	videoEncodingOptions, err := getVideoEncodingOptions(infoCopy.Video)
	if err != nil {
		return nil, err
	}

	if token == "" {
		token, err = ingress.BuildIngressToken(conf.ApiKey, conf.ApiSecret, info.RoomName, info.ParticipantIdentity, info.ParticipantName)
		if err != nil {
			return nil, err
		}
	}

	p := &Params{
		psrpcClient:          psrpcClient,
		IngressInfo:          infoCopy,
		Config:               conf,
		AudioEncodingOptions: audioEncodingOptions,
		VideoEncodingOptions: videoEncodingOptions,
		Token:                token,
		WsUrl:                wsUrl,
		RelayUrl:             relayUrl,
		TmpDir:               tmpDir,
		ExtraParams:          ep,
	}

	return p, nil
}

func getRTMPRelayUrl(conf *config.Config, resourceId string) string {
	return fmt.Sprintf("http://localhost:%d/rtmp/%s", conf.HTTPRelayPort, resourceId)
}

func getWHIPRelayUrlPrefix(conf *config.Config, resourceId string) string {
	return fmt.Sprintf("http://localhost:%d/whip/%s", conf.HTTPRelayPort, resourceId)
}

func getAudioEncodingOptions(options *livekit.IngressAudioOptions) (*livekit.IngressAudioEncodingOptions, error) {
	switch o := options.EncodingOptions.(type) {
	case nil:
		// default preset
		return getOptionsForAudioPreset(livekit.IngressAudioEncodingPreset_OPUS_STEREO_96KBPS)
	case *livekit.IngressAudioOptions_Preset:
		return getOptionsForAudioPreset(o.Preset)
	case *livekit.IngressAudioOptions_Options:
		return populateAudioEncodingOptionsDefaults(o.Options)
	default:
		return nil, errors.ErrInvalidAudioOptions
	}
}

func populateAudioEncodingOptionsDefaults(options *livekit.IngressAudioEncodingOptions) (*livekit.IngressAudioEncodingOptions, error) {
	o := proto.Clone(options).(*livekit.IngressAudioEncodingOptions)

	// Use Opus by default
	if o.AudioCodec == livekit.AudioCodec_DEFAULT_AC {
		o.AudioCodec = livekit.AudioCodec_OPUS
	}

	// Stereo by default
	if o.Channels == 0 {
		o.Channels = 2
	}

	// Default bitrate, depends on channel count
	if o.Bitrate == 0 {
		switch o.Channels {
		case 1:
			o.Bitrate = 64000
		default:
			o.Bitrate = 96000
		}
	}

	// DTX enabled by default

	return o, nil
}

func getVideoEncodingOptions(options *livekit.IngressVideoOptions) (*livekit.IngressVideoEncodingOptions, error) {
	switch o := options.EncodingOptions.(type) {
	case nil:
		// default preset
		return getOptionsForVideoPreset(livekit.IngressVideoEncodingPreset_H264_720P_30FPS_3_LAYERS)
	case *livekit.IngressVideoOptions_Preset:
		return getOptionsForVideoPreset(o.Preset)
	case *livekit.IngressVideoOptions_Options:
		return populateVideoEncodingOptionsDefaults(o.Options)
	default:
		return nil, errors.ErrInvalidVideoOptions
	}
}

func populateVideoEncodingOptionsDefaults(options *livekit.IngressVideoEncodingOptions) (*livekit.IngressVideoEncodingOptions, error) {
	o := proto.Clone(options).(*livekit.IngressVideoEncodingOptions)

	// Use Opus by default
	if o.VideoCodec == livekit.VideoCodec_DEFAULT_VC {
		o.VideoCodec = livekit.VideoCodec_H264_BASELINE
	}

	if o.FrameRate <= 0 {
		o.FrameRate = refFramerate
	}

	if len(o.Layers) == 0 {
		o.Layers = computeVideoLayers(&livekit.VideoLayer{
			Quality: livekit.VideoQuality_HIGH,
			Width:   1280,
			Height:  720,
			Bitrate: 1_700_000,
		}, 3)
	} else {
		for _, layer := range o.Layers {
			if layer.Bitrate == 0 {
				layer.Bitrate = getBitrateForParams(refBitrate, refWidth, refHeight, refFramerate,
					layer.Width, layer.Height, o.FrameRate)
			}
		}
	}

	return o, nil
}

func (p *Params) CopyInfo() *livekit.IngressInfo {
	p.stateLock.Lock()
	defer p.stateLock.Unlock()

	return proto.Clone(p.IngressInfo).(*livekit.IngressInfo)
}

// Useful in some paths where the extanded params are not known at creation time
func (p *Params) SetExtraParams(ep any) {
	p.ExtraParams = ep
}

func (p *Params) SetStatus(status livekit.IngressState_Status, errString string) {
	p.stateLock.Lock()
	defer p.stateLock.Unlock()

	p.State.Status = status
	p.State.Error = errString
}

func (p *Params) SetRoomId(roomId string) {
	p.stateLock.Lock()
	defer p.stateLock.Unlock()

	p.State.RoomId = roomId
}

func (p *Params) SetInputAudioState(ctx context.Context, audioState *livekit.InputAudioState, sendUpdateIfModified bool) {
	p.stateLock.Lock()
	modified := false

	if !proto.Equal(audioState, p.State.Audio) {
		modified = true
		p.State.Audio = audioState
	}
	p.stateLock.Unlock()

	if modified && sendUpdateIfModified {
		p.SendStateUpdate(ctx)
	}
}

func (p *Params) SetInputVideoState(ctx context.Context, videoState *livekit.InputVideoState, sendUpdateIfModified bool) {
	p.stateLock.Lock()
	modified := false

	if !proto.Equal(videoState, p.State.Video) {
		modified = true
		p.State.Video = videoState
	}
	p.stateLock.Unlock()

	if modified && sendUpdateIfModified {
		p.SendStateUpdate(ctx)
	}
}

func (p *Params) SendStateUpdate(ctx context.Context) {
	info := p.CopyInfo()

	_, err := p.psrpcClient.UpdateIngressState(ctx, &rpc.UpdateIngressStateRequest{
		IngressId: info.IngressId,
		State:     info.State,
	})
	if err != nil {
		logger.Errorw("failed to send update", err)
	}
}

func CopyRedactedIngressInfo(info *livekit.IngressInfo) *livekit.IngressInfo {
	infoCopy := proto.Clone(info).(*livekit.IngressInfo)

	infoCopy.StreamKey = utils.RedactIdentifier(infoCopy.StreamKey)

	return infoCopy
}
