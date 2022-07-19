package rtmp

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/logger"
)

const (
	defaultHTTPRelayPort = 9090
)

type RTMPRelay struct {
	rtmpServer *RTMPServer
}

func NewRTMPRelay(rtmpServer *RTMPServer) *RTMPRelay {
	return &RTMPRelay{
		rtmpServer: rtmpServer,
	}
}

func (r *RTMPRelay) Start(conf *config.Config) error {
	port := conf.HTTPRelayPort
	if port == 0 {
		port = defaultHTTPRelayPort
	}

	h := NewRTMPRelayHandler(r.rtmpServer)
	err := http.ListenAndServe(fmt.Sprintf(":%d", port), h)
	if err != nil {
		return err
	}

	return nil
}

type RTMPRelayHandler struct {
	rtmpServer *RTMPServer
}

func NewRTMPRelayHandler(rtmpServer *RTMPServer) *RTMPRelayHandler {
	return &RTMPRelayHandler{
		rtmpServer: rtmpServer,
	}
}

func (h *RTMPRelayHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var err error
	defer func() {
		fmt.Println("H", err)
		switch err {
		case errors.ErrIngressNotFound:
			w.WriteHeader(http.StatusNotFound)
		case nil:
			// Nothing, we already responded
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}()

	ingressId := strings.TrimLeft(r.URL.Path, "/")

	log := logger.Logger(logger.GetLogger().WithValues("ingressID", ingressId))
	log.Infow("relaying ingress")

	pr, pw := io.Pipe()
	bw := bufio.NewWriterSize(pw, 100)

	err = h.rtmpServer.AssociateRelay(ingressId, bw)
	if err != nil {
		return
	}
	defer func() {
		pw.Close()
		h.rtmpServer.DissociateRelay(ingressId)
	}()

	_, err = io.Copy(w, pr)
	fmt.Println("Copy", err)

}
