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
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/ipc"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/ingress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
	"github.com/livekit/psrpc"
)

type Params struct {
	stateLock   sync.Mutex
	psrpcClient rpc.IOInfoClient

	*livekit.IngressInfo
	*config.Config

	// extra state
	err error

	logger logger.Logger

	AudioEncodingOptions *livekit.IngressAudioEncodingOptions
	VideoEncodingOptions *livekit.IngressVideoEncodingOptions
	Live                 bool

	// connection info
	WsUrl string
	Token string

	// extra logging fields
	LoggingFields map[string]string

	// relay info
	RelayUrl   string
	RelayToken string
	TmpDir     string

	FeatureFlags map[string]string

	// Input type specific private parameters
	ExtraParams any
}

type WhipExtraParams struct {
	MimeTypes map[types.StreamKind]string `json:"mime_types"`
}

func InitLogger(conf *config.Config, info *livekit.IngressInfo, loggingFields map[string]string) error {
	fields := getLoggerFields(info, loggingFields)

	err := conf.InitLogger(fields...)
	if err != nil {
		return err
	}

	return nil
}
func GetParams(ctx context.Context, psrpcClient rpc.IOInfoClient, conf *config.Config, info *livekit.IngressInfo, wsUrl, token, relayToken string, featureFlags map[string]string, loggingFields map[string]string, ep any) (*Params, error) {
	var err error

	// The state should have been created by the service, before launching the hander, but be defensive here.
	if info.State == nil || info.State.ResourceId == "" {
		return nil, errors.ErrMissingResourceId
	}

	relayUrl := ""
	switch info.InputType {
	case livekit.IngressInput_RTMP_INPUT:
		relayUrl = getRTMPRelayUrl(conf, info.State.ResourceId)
	case livekit.IngressInput_WHIP_INPUT:
		relayUrl = getWHIPRelayUrlPrefix(conf, info.State.ResourceId)
	}

	if relayToken == "" {
		relayToken = utils.NewGuid("")
	}

	l := logger.GetLogger().WithValues(getLoggerFields(info, loggingFields)...)

	tmpDir := path.Join(os.TempDir(), info.State.ResourceId)

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

	UpdateTranscodingEnabled(infoCopy)

	if token == "" {
		token, err = ingress.BuildIngressToken(conf.ApiKey, conf.ApiSecret, info.RoomName, info.ParticipantIdentity, info.ParticipantName, info.ParticipantMetadata, info.IngressId)
		if err != nil {
			return nil, err
		}
	}

	p := &Params{
		psrpcClient:          psrpcClient,
		IngressInfo:          infoCopy,
		logger:               l,
		Config:               conf,
		AudioEncodingOptions: audioEncodingOptions,
		VideoEncodingOptions: videoEncodingOptions,
		Live:                 getLive(info),
		Token:                token,
		WsUrl:                wsUrl,
		RelayToken:           relayToken,
		LoggingFields:        loggingFields,
		RelayUrl:             relayUrl,
		TmpDir:               tmpDir,
		FeatureFlags:         featureFlags,
		ExtraParams:          ep,
	}

	return p, nil
}

func UpdateTranscodingEnabled(info *livekit.IngressInfo) {
	if info.EnableTranscoding != nil {
		return
	}

	// Backward compatibility. This is an ingress created before the EnableTranscoding field was added.
	// Default to enabling transcoding for WHIP
	switch info.InputType {
	case livekit.IngressInput_WHIP_INPUT:
		b := !info.BypassTranscoding
		info.EnableTranscoding = &b
	default:
		t := true
		info.EnableTranscoding = &t
	}
}

func getLive(info *livekit.IngressInfo) bool {
	switch info.InputType {
	case livekit.IngressInput_URL_INPUT:
		if strings.HasPrefix(info.Url, "http://") || strings.HasPrefix(info.Url, "https://") {
			return false
		} else {
			return true
		}
	default:
		// TODO RTMP and WHIP should use the live mode but more work is needed on the pipeline sample timestamp fugding/dropping to avoid A/V sync issues
		return false
	}
}

func getLoggerFields(info *livekit.IngressInfo, loggingFields map[string]string) []interface{} {
	fields := []interface{}{"ingressID", info.IngressId, "resourceID", info.State.ResourceId, "roomName", info.RoomName, "participantIdentity", info.ParticipantIdentity}
	for k, v := range loggingFields {
		fields = append(fields, k, v)
	}

	return fields
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

	info := proto.Clone(p.IngressInfo).(*livekit.IngressInfo)
	if info.State != nil && p.err != nil {
		info.State.Error = p.err.Error()
	}

	return info
}

// Useful in some paths where the extanded params are not known at creation time
func (p *Params) SetExtraParams(ep any) {
	p.ExtraParams = ep
}

func (p *Params) SetStatus(status livekit.IngressState_Status, err error) {
	p.stateLock.Lock()
	defer p.stateLock.Unlock()

	p.State.Status = status
	// Always return the first error
	if p.err == nil {
		p.err = err
	}

	switch status {
	case livekit.IngressState_ENDPOINT_COMPLETE,
		livekit.IngressState_ENDPOINT_INACTIVE,
		livekit.IngressState_ENDPOINT_ERROR:
		if p.State.EndedAt == 0 {
			p.State.EndedAt = time.Now().UnixNano()
		}
	}
}

func (p *Params) SetRoomId(roomId string) {
	p.stateLock.Lock()
	defer p.stateLock.Unlock()

	p.State.RoomId = roomId
}

func (p *Params) SetInputAudioState(ctx context.Context, audioState *livekit.InputAudioState, sendUpdateIfModified bool) {
	p.stateLock.Lock()
	modified := false

	// Do not overwrite the bitrate
	if audioState != nil && p.State.Audio != nil {
		audioState.AverageBitrate = p.State.Audio.AverageBitrate
	}

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

	// Do not overwrite the bitrate
	if videoState != nil && p.State.Video != nil {
		videoState.AverageBitrate = p.State.Video.AverageBitrate
	}

	if !proto.Equal(videoState, p.State.Video) {
		modified = true
		p.State.Video = videoState
	}
	p.stateLock.Unlock()

	if modified && sendUpdateIfModified {
		p.SendStateUpdate(ctx)
	}
}

func (p *Params) SetInputAudioStats(st *ipc.TrackStats) {
	p.stateLock.Lock()

	if p.State.Audio == nil {
		p.State.Audio = &livekit.InputAudioState{}
	}

	p.State.Audio.AverageBitrate = st.AverageBitrate

	p.stateLock.Unlock()
}

func (p *Params) SetInputVideoStats(st *ipc.TrackStats) {
	p.stateLock.Lock()

	if p.State.Video == nil {
		p.State.Video = &livekit.InputVideoState{}
	}

	p.State.Video.AverageBitrate = st.AverageBitrate

	p.stateLock.Unlock()
}

func (p *Params) SendStateUpdate(ctx context.Context) {
	info := p.CopyInfo()

	info.State.UpdatedAt = time.Now().UnixNano()

	_, err := p.psrpcClient.UpdateIngressState(ctx, &rpc.UpdateIngressStateRequest{
		IngressId: info.IngressId,
		State:     info.State,
	})
	if err != nil {
		var psrpcErr psrpc.Error
		if !errors.As(err, &psrpcErr) || psrpcErr.Code() != psrpc.NotFound {
			// Ingress was deleted
			p.logger.Errorw("failed to send update", err)
		}
	}
}

func (p *Params) GetLogger() logger.Logger {
	return p.logger
}

func CopyRedactedIngressInfo(info *livekit.IngressInfo) *livekit.IngressInfo {
	infoCopy := proto.Clone(info).(*livekit.IngressInfo)

	infoCopy.StreamKey = utils.RedactIdentifier(infoCopy.StreamKey)

	return infoCopy
}
