package service

import (
	"fmt"
	"net/http"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/rtmp"
	"github.com/livekit/ingress/pkg/whip"
	"github.com/livekit/protocol/logger"
)

type Relay struct {
	server     *http.Server
	rtmpServer *rtmp.RTMPServer
	whipServer *whip.WHIPServer
}

func NewRelay(rtmpServer *rtmp.RTMPServer, whipServer *whip.WHIPServer) *Relay {
	return &Relay{
		rtmpServer: rtmpServer,
		whipServer: whipServer,
	}
}

func (r *Relay) Start(conf *config.Config) error {
	port := conf.HTTPRelayPort

	mux := http.NewServeMux()

	if r.rtmpServer != nil {
		h := rtmp.NewRTMPRelayHandler(r.rtmpServer)
		mux.Handle("/rtmp/", h)
	}
	if r.whipServer != nil {
		h := whip.NewWHIPRelayHandler(r.whipServer)
		mux.Handle("/whip/", h)
	}

	r.server = &http.Server{
		Handler: mux,
		Addr:    fmt.Sprintf(":%d", port),
	}

	go func() {
		err := r.server.ListenAndServe()
		logger.Debugw("Relay stopped", "error", err)
	}()

	return nil
}

func (r *Relay) Stop() error {
	return r.server.Close()
}
