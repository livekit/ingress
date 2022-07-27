package media

import (
	"context"
	"fmt"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/rtmp"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils"
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

func Validate(ctx context.Context, conf *config.Config, req *livekit.StartIngressRequest) (*livekit.IngressInfo, error) {
	sk := utils.NewGuid("")

	p, err := getParams(ctx, conf, req, sk)

	p.Url = rtmp.NewUrl(p.IngressInfo.StreamKey)
	return p.IngressInfo, err
}

func GetParams(ctx context.Context, conf *config.Config, req *livekit.StartIngressRequest, url string, streamKey string) (*Params, error) {
	p, err := getParams(ctx, conf, req, streamKey)
	if err != nil {
		return nil, err
	}
	p.Url = url
	return p, nil
}

func getParams(ctx context.Context, conf *config.Config, req *livekit.StartIngressRequest, streamKey string) (p *Params, err error) {
	p = &Params{
		IngressInfo: &livekit.IngressInfo{
			IngressId:           req.IngressId,
			Name:                req.Request.Name,
			StreamKey:           streamKey,
			Url:                 "TODO",
			InputType:           livekit.IngressInput_RTMP_INPUT,
			Audio:               req.Request.Audio,
			Video:               req.Request.Video,
			RoomName:            req.Request.RoomName,
			ParticipantIdentity: req.Request.ParticipantIdentity,
			ParticipantName:     req.Request.ParticipantName,
			Reusable:            false,
			State: &livekit.IngressState{
				Status: livekit.IngressState_ENDPOINT_INACTIVE,
			},
		},
		Logger:    logger.Logger(logger.GetLogger().WithValues("ingressID", req.IngressId)),
		WsUrl:     conf.WsUrl,
		ApiKey:    conf.ApiKey,
		ApiSecret: conf.ApiSecret,
		RelayUrl:  getRelayUrl(conf, streamKey),
		GstReady:  make(chan struct{}),
	}

	return
}

func getRelayUrl(conf *config.Config, streamKey string) string {
	return fmt.Sprintf("http://localhost:%d/%s", conf.HTTPRelayPort, streamKey)
}
