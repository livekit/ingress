package rtmp

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/psrpc"
)

type RTMPRelay struct {
	server     *http.Server
	rtmpServer *RTMPServer
}

func NewRTMPRelay(rtmpServer *RTMPServer) *RTMPRelay {
	return &RTMPRelay{
		rtmpServer: rtmpServer,
	}
}

func (r *RTMPRelay) Start(conf *config.Config) error {
	port := conf.HTTPRelayPort

	h := NewRTMPRelayHandler(r.rtmpServer)
	r.server = &http.Server{
		Handler: h,
		Addr:    fmt.Sprintf(":%d", port),
	}

	go func() {
		err := r.server.ListenAndServe()
		logger.Debugw("RTMP relay stopped", "error", err)
	}()

	return nil
}

func (r *RTMPRelay) Stop() error {
	return r.server.Close()
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
		var psrpcErr psrpc.Error

		switch {
		case errors.As(err, &psrpcErr):
			w.WriteHeader(psrpcErr.ToHttp())
		case err == nil:
			// Nothing, we already responded
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}()

	streamKey := strings.TrimLeft(r.URL.Path, "/")

	log := logger.Logger(logger.GetLogger().WithValues("streamKey", streamKey))
	log.Infow("relaying ingress")

	pr, pw := io.Pipe()
	done := make(chan error)

	go func() {
		_, err = io.Copy(w, pr)
		done <- err
		close(done)
	}()

	err = h.rtmpServer.AssociateRelay(streamKey, pw)
	if err != nil {
		return
	}
	defer func() {
		pw.Close()
		h.rtmpServer.DissociateRelay(streamKey)
	}()

	err = <-done
}
