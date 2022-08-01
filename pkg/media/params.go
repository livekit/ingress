package media

import (
	"context"
	"fmt"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

type Params struct {
	*livekit.IngressInfo

	Logger logger.Logger

	// connection info
	WsUrl     string
	ApiKey    string
	ApiSecret string

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

func GetParams(ctx context.Context, conf *config.Config, info *livekit.IngressInfo) (*Params, error) {
	infoCopy := *info
	infoCopy.State = &livekit.IngressState{
		Status: livekit.IngressState_ENDPOINT_INACTIVE,
	}

	p := &Params{
		IngressInfo: &infoCopy,
		Logger:      logger.Logger(logger.GetLogger().WithValues("ingressID", info.IngressId)),
		WsUrl:       conf.WsUrl,
		ApiKey:      conf.ApiKey,
		ApiSecret:   conf.ApiSecret,
		RelayUrl:    getRelayUrl(conf, info.StreamKey),
		GstReady:    make(chan struct{}),
	}

	return p, nil
}

func getRelayUrl(conf *config.Config, streamKey string) string {
	return fmt.Sprintf("http://localhost:%d/%s", conf.HTTPRelayPort, streamKey)
}
