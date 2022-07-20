package media

import (
	"context"
	"fmt"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/rtmp"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

type Params struct {
	*livekit.IngressInfo

	Logger logger.Logger

	AudioOptions *livekit.IngressAudioOptions
	VideoOptions *livekit.IngressVideoOptions

	// connection info
	WsUrl     string
	ApiKey    string
	ApiSecret string

	// relay info
	RelayUrl string

	GstReady chan struct{}
}

func Validate(ctx context.Context, conf *config.Config, req *livekit.StartIngressRequest) (*livekit.IngressInfo, error) {
	p, err := getParams(ctx, conf, req)
	p.Url = rtmp.NewUrl(req.IngressId)
	return p.IngressInfo, err
}

func GetParams(ctx context.Context, conf *config.Config, req *livekit.StartIngressRequest, url string) (*Params, error) {
	p, err := getParams(ctx, conf, req)
	if err != nil {
		return nil, err
	}
	p.Url = url
	return p, nil
}

func getParams(ctx context.Context, conf *config.Config, req *livekit.StartIngressRequest) (p *Params, err error) {
	p = &Params{
		IngressInfo: &livekit.IngressInfo{
			IngressId:           req.IngressId,
			Name:                req.Request.Name,
			InputType:           0,
			Status:              livekit.IngressInfo_ENDPOINT_WAITING,
			InputStatus:         nil,
			Room:                req.Request.RoomName,
			ParticipantIdentity: req.Request.ParticipantIdentity,
			ParticipantName:     req.Request.ParticipantName,
			Url:                 "",
			Tracks:              nil,
		},
		Logger:       logger.Logger(logger.GetLogger().WithValues("ingressID", req.IngressId)),
		AudioOptions: req.Request.Audio,
		VideoOptions: req.Request.Video,
		WsUrl:        conf.WsUrl,
		ApiKey:       conf.ApiKey,
		ApiSecret:    conf.ApiSecret,
		RelayUrl:     getRelayUrl(conf, req),
		GstReady:     make(chan struct{}),
	}

	return
}

func getRelayUrl(conf *config.Config, req *livekit.StartIngressRequest) string {
	return fmt.Sprintf("http://localhost:%d/%s", conf.HTTPRelayPort, req.IngressId)
}
