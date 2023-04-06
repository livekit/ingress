package media

import (
	"context"
	"fmt"
	"time"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/ingress"
	"github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/proto"
)

type Params struct {
	*livekit.IngressInfo

	AudioEncodingOptions *livekit.IngressAudioEncodingOptions
	VideoEncodingOptions *livekit.IngressVideoEncodingOptions

	// connection info
	WsUrl string
	Token string

	// relay info
	RelayUrl string
}

func Validate(ctx context.Context, info *livekit.IngressInfo) error {
	// TODO validate encoder settings

	if info.InputType != livekit.IngressInput_RTMP_INPUT {
		return errors.ErrInvalidIngress("unsupported input type")
	}

	if info.StreamKey == "" {
		return errors.ErrInvalidIngress("no stream key")
	}

	// For now, require a room to be set. We should eventually allow changing the room on an active ingress
	if info.RoomName == "" {
		return errors.ErrInvalidIngress("no room name")
	}

	if info.ParticipantIdentity == "" {
		return errors.ErrInvalidIngress("no participant identity")
	}

	err := ingress.ValidateVideoOptionsConsistency(info.Video)
	if err != nil {
		return err
	}

	err = ingress.ValidateAudioOptionsConsistency(info.Audio)
	if err != nil {
		return err
	}

	return nil
}

func GetParams(ctx context.Context, conf *config.Config, info *livekit.IngressInfo, wsUrl string, token string) (*Params, error) {
	var err error

	err = conf.InitLogger("ingressID", info.IngressId)
	if err != nil {
		return nil, err
	}

	err = Validate(ctx, info)
	if err != nil {
		return nil, err
	}

	infoCopy := proto.Clone(info).(*livekit.IngressInfo)

	// The state should have been created by the service, before launching the hander, but be defensive here.
	if infoCopy.State == nil {
		infoCopy.State = &livekit.IngressState{
			Status:    livekit.IngressState_ENDPOINT_BUFFERING,
			StartedAt: time.Now().UnixNano(),
		}
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
		IngressInfo:          infoCopy,
		AudioEncodingOptions: audioEncodingOptions,
		VideoEncodingOptions: videoEncodingOptions,
		Token:                token,
		WsUrl:                wsUrl,
		RelayUrl:             getRelayUrl(conf, info.StreamKey),
	}

	return p, nil
}

func getRelayUrl(conf *config.Config, streamKey string) string {
	return fmt.Sprintf("http://localhost:%d/%s", conf.HTTPRelayPort, streamKey)
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

func (p *Params) SetStatus(status livekit.IngressState_Status, errString string) {
	p.State.Status = status
	p.State.Error = errString
}

func (p *Params) SetRoomId(roomId string) {
	p.State.RoomId = roomId
}
