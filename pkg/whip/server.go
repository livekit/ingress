// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package whip

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
	"github.com/livekit/psrpc"
)

const (
	sdpResponseTimeout  = 5 * time.Second
	sessionStartTimeout = 10 * time.Second
	rpcTimeout          = 5 * time.Second
)

type HealthHandlers map[string]http.HandlerFunc

type WHIPServer struct {
	ctx    context.Context
	cancel context.CancelFunc

	conf         *config.Config
	webRTCConfig *rtcconfig.WebRTCConfig
	onPublish    func(streamKey, resourceId string, ihs rpc.IngressHandlerServerImpl) (*params.Params, func(mimeTypes map[types.StreamKind]string, err error) *stats.LocalMediaStatsGatherer, func(error), error)
	rpcClient    rpc.IngressHandlerClient

	handlersLock sync.Mutex
	handlers     map[string]*whipHandler
}

func NewWHIPServer(rpcClient rpc.IngressHandlerClient) *WHIPServer {
	return &WHIPServer{
		rpcClient: rpcClient,
		handlers:  make(map[string]*whipHandler),
	}
}

func (s *WHIPServer) Start(
	conf *config.Config,
	onPublish func(streamKey, resourceId string, ihs rpc.IngressHandlerServerImpl) (*params.Params, func(mimeTypes map[types.StreamKind]string, err error) *stats.LocalMediaStatsGatherer, func(error), error),
	healthHandlers HealthHandlers,
) error {
	s.ctx, s.cancel = context.WithCancel(context.Background())

	logger.Infow("starting WHIP server")

	if onPublish == nil {
		return psrpc.NewErrorf(psrpc.Internal, "no onPublish callback provided")
	}

	s.onPublish = onPublish
	s.conf = conf

	var err error
	s.webRTCConfig, err = rtcconfig.NewWebRTCConfig(&conf.RTCConfig, conf.Development)
	if err != nil {
		return err
	}

	r := mux.NewRouter()

	r.HandleFunc("/{app}", func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func() {
			s.handleError(err, w)
		}()

		bearer := r.Header.Get("Authorization")
		// OBS adds the 'Bearer' prefix as expected, but some other clients do not
		streamKey := strings.TrimPrefix(bearer, "Bearer ")

		err = s.handleNewWhipClient(w, r, streamKey)
	}).Methods("POST")

	r.HandleFunc("/{app}/{stream_key}", func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func() {
			s.handleError(err, w)
		}()

		streamKey := mux.Vars(r)["stream_key"]

		err = s.handleNewWhipClient(w, r, streamKey)
	}).Methods("POST")

	r.HandleFunc("/{app}", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w, r, false)
		w.WriteHeader(http.StatusNoContent)
	}).Methods("OPTIONS")

	r.HandleFunc("/{app}/{stream_key}", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w, r, false)
		w.WriteHeader(http.StatusNoContent)
	}).Methods("OPTIONS")

	// End
	r.HandleFunc("/{app}/{stream_key}/{resource_id}", func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func() {
			s.handleError(err, w)
		}()

		vars := mux.Vars(r)
		streamKey := vars["stream_key"]
		resourceID := vars["resource_id"]

		logger.Infow("handling WHIP delete request", "resourceID", resourceID)

		req := &rpc.DeleteWHIPResourceRequest{
			ResourceId: resourceID,
			StreamKey:  streamKey,
		}

		w.Header().Set("Access-Control-Allow-Origin", "*")

		_, err = s.rpcClient.DeleteWHIPResource(s.ctx, resourceID, req, psrpc.WithRequestTimeout(5*time.Second))
		if err == psrpc.ErrNoResponse {
			err = errors.ErrIngressNotFound
		}
	}).Methods("DELETE")

	// Trickle, ICE Restart unimplemented for now
	r.HandleFunc("/{app}/{stream_key}/{resource_id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	}).Methods("PATCH")

	r.HandleFunc("/{app}/{stream_key}/{resource_id}", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w, r, true)
	}).Methods("OPTIONS")

	// Expose the health endpoints on the WHIP server as well to make
	// deployment as a k8s ingress more straightforward
	for path, handler := range healthHandlers {
		r.HandleFunc(path, handler).Methods("GET")
	}

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

func (s *WHIPServer) CloseHandler(resourceId string) {
	s.handlersLock.Lock()
	h, ok := s.handlers[resourceId]
	s.handlersLock.Unlock()

	if ok && h != nil {
		h.Close()
	}
}

func (s *WHIPServer) Stop() {
	s.cancel()
}

func (s *WHIPServer) AssociateRelay(resourceId string, kind types.StreamKind, token string, w io.WriteCloser) error {
	s.handlersLock.Lock()
	h, ok := s.handlers[resourceId]
	s.handlersLock.Unlock()
	if ok && h != nil {
		err := h.AssociateRelay(kind, token, w)
		if err != nil {
			return err
		}
	} else {
		return errors.ErrIngressNotFound
	}

	return nil
}

func (s *WHIPServer) DissociateRelay(resourceId string, kind types.StreamKind) {
	s.handlersLock.Lock()
	h, ok := s.handlers[resourceId]
	s.handlersLock.Unlock()
	if ok && h != nil {
		h.DissociateRelay(kind)
	}
}

func (s *WHIPServer) IsIdle() bool {
	s.handlersLock.Lock()
	defer s.handlersLock.Unlock()

	return len(s.handlers) == 0
}

func (s *WHIPServer) handleError(err error, w http.ResponseWriter) {
	var psrpcErr psrpc.Error
	switch {
	case errors.As(err, &psrpcErr):
		w.WriteHeader(psrpcErr.ToHttp())
		_, _ = w.Write([]byte(psrpcErr.Error()))
	case err == nil:
		// Nothing, we already responded
	default:
		logger.Debugw("whip request failed", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func (s *WHIPServer) handleNewWhipClient(w http.ResponseWriter, r *http.Request, streamKey string) error {
	// TODO return ETAG header

	vars := mux.Vars(r)
	app := vars["app"]

	sdpOffer := bytes.Buffer{}

	_, err := io.Copy(&sdpOffer, r.Body)
	if err != nil {
		return err
	}

	logger.Debugw("new whip request", "streamKey", streamKey, "sdpOffer", sdpOffer.String())

	resourceId, sdp, err := s.createStream(streamKey, sdpOffer.String())
	if err != nil {
		return err
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", "Location")
	w.Header().Set("Content-Type", "application/sdp")
	w.Header().Set("Location", fmt.Sprintf("/%s/%s/%s", app, streamKey, resourceId))
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(sdp))

	return nil
}

func (s *WHIPServer) createStream(streamKey string, sdpOffer string) (string, string, error) {
	ctx, done := context.WithTimeout(s.ctx, sdpResponseTimeout)
	defer done()

	resourceId := utils.NewGuid(utils.WHIPResourcePrefix)

	h := NewWHIPHandler(s.webRTCConfig)

	p, ready, ended, err := s.onPublish(streamKey, resourceId, h)
	if err != nil {
		return "", "", err
	}

	sdpResponse, err := h.Init(ctx, p, sdpOffer)
	if err != nil {
		return "", "", err
	}

	go func() {
		ctx, done := context.WithTimeout(s.ctx, sessionStartTimeout)
		defer done()

		var err error
		var mimeTypes map[types.StreamKind]string
		if ready != nil {
			defer func() {
				stats := ready(mimeTypes, err)
				if stats != nil {
					h.SetMediaStatsGatherer(stats)
				}

				if err != nil {
					s.handlersLock.Lock()
					delete(s.handlers, resourceId)
					s.handlersLock.Unlock()
				}
			}()
		}

		s.handlersLock.Lock()
		s.handlers[resourceId] = h
		s.handlersLock.Unlock()

		mimeTypes, err = h.Start(ctx)
		if err != nil {
			return
		}

		logger.Infow("all tracks ready")

		go func() {
			var err error
			defer func() {
				s.handlersLock.Lock()
				delete(s.handlers, resourceId)
				s.handlersLock.Unlock()

				if err != nil {
					logger.Warnw("WHIP session failed", err, "streamKey", streamKey, "resourceID", resourceId)
				}

				if ended != nil {
					ended(err)
				}
			}()

			err = h.WaitForSessionEnd(s.ctx)
		}()
	}()

	return resourceId, sdpResponse, nil
}

func setCORSHeaders(w http.ResponseWriter, r *http.Request, resourceEndpoint bool) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	if resourceEndpoint {
		w.Header().Set("Access-Control-Allow-Methods", "PATCH, OPTIONS, DELETE")
	} else {
		w.Header().Set("Accept-Post", "application/sdp")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Expose-Headers", "Location")
	}
}
