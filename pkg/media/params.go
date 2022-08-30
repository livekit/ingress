package media

import (
	"context"
	"fmt"

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
	infoCopy.State = &livekit.IngressState{
		Status: livekit.IngressState_ENDPOINT_INACTIVE,
	}

	if infoCopy.Audio == nil {
		infoCopy.Audio = getDefaultAudioParams()
	}
	if infoCopy.Video == nil {
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
		},
	}
}
