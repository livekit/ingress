package media

import (
	"context"
	"fmt"
	"time"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/ingress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/pion/webrtc/v3"
)

type Params struct {
	*livekit.IngressInfo

	Logger logger.Logger

	// connection info
	WsUrl string
	Token string

	// relay info
	RelayUrl string

	GstReady chan struct{}
}

func Validate(ctx context.Context, info *livekit.IngressInfo) error {
	if info.InputType != livekit.IngressInput_RTMP_INPUT {
		return errors.NewInvalidIngressError("unsupported input type")
	}

	if info.StreamKey == "" {
		return errors.NewInvalidIngressError("no stream key")
	}

	// For now, require a room to be set. We should eventually allow changing the room on an active ingress
	if info.RoomName == "" {
		return errors.NewInvalidIngressError("no room name")
	}

	if info.ParticipantIdentity == "" {
		return errors.NewInvalidIngressError("no participant identity")
	}

	return nil
}

func GetParams(ctx context.Context, conf *config.Config, info *livekit.IngressInfo, wsUrl string, token string) (*Params, error) {
	var err error

	infoCopy := *info

	// The state should have been created by the service, before launching the hander, but be defensive here.
	if infoCopy.State == nil {
		infoCopy.State = &livekit.IngressState{
			Status:    livekit.IngressState_ENDPOINT_BUFFERING,
			StartedAt: time.Now().UnixNano(),
		}
	}

	if !isValidAudioParams(infoCopy.Audio) {
		infoCopy.Audio = getDefaultAudioParams()
	}
	if !isValidVideoParams(infoCopy.Video) {
		infoCopy.Video = getDefaultVideoParams()
	}

	if wsUrl == "" {
		wsUrl = conf.WsUrl
	}

	if token == "" {
		token, err = ingress.BuildIngressToken(conf.ApiKey, conf.ApiSecret, info.RoomName, info.ParticipantIdentity, info.ParticipantName)
		if err != nil {
			return nil, err
		}
	}

	p := &Params{
		IngressInfo: &infoCopy,
		Logger:      logger.Logger(logger.GetLogger().WithValues("ingressID", info.IngressId)),
		Token:       token,
		WsUrl:       wsUrl,
		RelayUrl:    getRelayUrl(conf, info.StreamKey),
		GstReady:    make(chan struct{}),
	}

	return p, nil
}

func getRelayUrl(conf *config.Config, streamKey string) string {
	return fmt.Sprintf("http://localhost:%d/%s", conf.HTTPRelayPort, streamKey)
}

func isValidAudioParams(options *livekit.IngressAudioOptions) bool {
	if options == nil {
		return false
	}

	if options.MimeType == "" {
		return false
	}

	if options.Bitrate == 0 {
		return false
	}

	if options.Channels == 0 {
		return false
	}

	return true
}

func getDefaultAudioParams() *livekit.IngressAudioOptions {
	return &livekit.IngressAudioOptions{
		Name:       "audio",
		Source:     0,
		MimeType:   webrtc.MimeTypeOpus,
		Bitrate:    64000,
		DisableDtx: false,
		Channels:   2,
	}
}

func isValidVideoParams(options *livekit.IngressVideoOptions) bool {
	if options == nil {
		return false
	}

	if len(options.Layers) == 0 {
		return false
	}

	return true
}

func getDefaultVideoParams() *livekit.IngressVideoOptions {
	return &livekit.IngressVideoOptions{
		Name:     "video",
		Source:   0,
		MimeType: webrtc.MimeTypeH264,
		Layers: []*livekit.VideoLayer{
			{
				Quality: livekit.VideoQuality_HIGH,
				Width:   1280,
				Height:  720,
				Bitrate: 3000,
			},
			{
				Quality: livekit.VideoQuality_MEDIUM,
				Width:   960,
				Height:  540,
				Bitrate: 1500,
			},
			{
				Quality: livekit.VideoQuality_LOW,
				Width:   640,
				Height:  320,
				Bitrate: 750,
			},
		},
	}
}

func (p *Params) SetStatus(status livekit.IngressState_Status, errString string) {
	p.State.Status = status
	p.State.Error = errString
}

func (p *Params) SetRoomId(roomId string) {
	p.State.RoomId = roomId
}
