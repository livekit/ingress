package whip

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils"
	"github.com/livekit/psrpc"
)

const (
	sdpResponseTimeout = 5 * time.Second
)

type WHIPServer struct {
	onPublish func(streamKey, resourceId, sdpOffer string) error
	handlers  sync.Map
}

type handler struct {
	sdpChan chan<- sdpRes
}

type sdpRes struct {
	sdp string
	err error
}

func NewWHIPServer() *WHIPServer {
	return &WHIPServer{}
}

func (s *WHIPServer) Start(conf *config.Config, onPublish func(streamKey, resourceId, sdpOffer string) error) error {
	s.onPublish = onPublish

	r := mux.NewRouter()

	r.HandleFunc("/{app}/{stream_key}", func(w http.ResponseWriter, r *http.Request) {
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

		vars := mux.Vars(r)
		app := vars["app"]
		streamKey := vars["stream_key"]

		sdpOffer := bytes.Buffer{}

		_, err = io.Copy(&sdpOffer, r.Body)
		if err != nil {
			return
		}

		resourceId, sdp, err := s.createStream(streamKey, string(sdpOffer.Bytes()))
		if err != nil {
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/sdp")
		w.Header().Set("Location", fmt.Sprintf("/%s/%s/%s", app, streamKey, resourceId))
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(sdp))

	}).Methods("POST")

	r.HandleFunc("/{app}/{stream_key}/{resource_id}", func(w http.ResponseWriter, r *http.Request) {
		// RPC call
	}).Methods("DELETE")

	r.HandleFunc("/{app}/{stream_key}/{resource_id}", func(w http.ResponseWriter, r *http.Request) {
		// RPC call
	}).Methods("PATCH")

	hs := &http.Server{
		Addr:         fmt.Sprintf(":%d", conf.WHIPPort),
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		err := hs.ListenAndServe()
		if err != http.ErrServerClosed {
			logger.Errorw("WHIP server start failed", err)
		}
	}()

	return nil
}

func (s *WHIPServer) SetSDPResponse(resourceId string, sdp string, err error) error {
	entry, ok := s.handlers.Load(resourceId)
	if !ok {
		return psrpc.NewErrorf(psrpc.NotFound, "unknown resource id")
	}
	sdpChan := entry.(chan sdpRes)

	select {
	case sdpChan <- sdpRes{sdp: sdp, err: err}:
		// success
	default:
		return psrpc.NewErrorf(psrpc.Internal, "SDP response channel full")
	}

	return nil
}

func (s *WHIPServer) createStream(streamKey string, sdpOffer string) (string, string, error) {
	resourceId := utils.NewGuid(utils.WHIPResourcePrefix)

	if s.onPublish != nil {
		err := s.onPublish(streamKey, resourceId, sdpOffer)
		if err != nil {
			return "", "", err
		}
	}

	sdpChan := make(chan sdpRes)
	s.handlers.Store(resourceId, sdpChan)
	defer s.handlers.Delete(resourceId)

	var sdp string
	select {
	case res := <-sdpChan:
		if res.err != nil {
			return resourceId, "", res.err
		}
		sdp = res.sdp
	case <-time.After(sdpResponseTimeout):
		return resourceId, "", psrpc.NewErrorf(psrpc.Unavailable, "timeout waiting for sdp offer")
	}

	return resourceId, sdp, nil
}
