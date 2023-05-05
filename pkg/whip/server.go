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
	"github.com/livekit/ingress/pkg/types"
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

type WHIPServer struct {
	ctx    context.Context
	cancel context.CancelFunc

	conf      *config.Config
	onPublish func(streamKey, resourceId string) (func(mimeTypes map[types.StreamKind]string, err error), error)
	handlers  sync.Map
	rpcClient rpc.IngressHandlerClient
}

func NewWHIPServer(rpcClient rpc.IngressHandlerClient) *WHIPServer {
	return &WHIPServer{
		rpcClient: rpcClient,
	}
}

func (s *WHIPServer) Start(
	conf *config.Config,
	onPublish func(streamKey, resourceId string) (func(mimeTypes map[types.StreamKind]string, err error), error),
) error {
	s.ctx, s.cancel = context.WithCancel(context.Background())

	s.onPublish = onPublish
	s.conf = conf

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
		resourceID := vars["resource_id"]

		logger.Infow("handling WHIP delete request", "resourceID", resourceID)

		req := &rpc.DeleteWHIPResourceRequest{
			ResourceId: resourceID,
		}

		w.Header().Set("Access-Control-Allow-Origin", "*")

		_, err = s.rpcClient.DeleteWHIPResource(s.ctx, resourceID, req, psrpc.WithRequestTimeout(5*time.Second))
	}).Methods("DELETE")

	// Trickle, ICE Restart
	//	r.HandleFunc("/{app}/{stream_key}/{resource_id}", func(w http.ResponseWriter, r *http.Request) {
	//		// RPC call
	//	}).Methods("PATCH")

	r.HandleFunc("/{app}/{stream_key}/{resource_id}", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w, r, true)
	}).Methods("OPTIONS")

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

func (s *WHIPServer) Stop() {
	s.cancel()
}

func (s *WHIPServer) AssociateRelay(resourceId string, kind types.StreamKind, w io.WriteCloser) error {
	h, ok := s.handlers.Load(resourceId)
	if ok && h != nil {
		err := h.(*whipHandler).AssociateRelay(kind, w)
		if err != nil {
			return err
		}
	} else {
		return errors.ErrIngressNotFound
	}

	return nil
}

func (s *WHIPServer) DissociateRelay(resourceId string, kind types.StreamKind) error {
	h, ok := s.handlers.Load(resourceId)
	if ok && h != nil {
		err := h.(*whipHandler).DissociateRelay(kind)
		if err != nil {
			return err
		}
	} else {
		return errors.ErrIngressNotFound
	}

	return nil
}

func (s *WHIPServer) handleError(err error, w http.ResponseWriter) {
	var psrpcErr psrpc.Error
	switch {
	case errors.As(err, &psrpcErr):
		w.WriteHeader(psrpcErr.ToHttp())
		w.Write([]byte(psrpcErr.Error()))
	case err == nil:
		// Nothing, we already responded
	default:
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

	resourceId, sdp, err := s.createStream(streamKey, string(sdpOffer.Bytes()))
	if err != nil {
		return err
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", "Location")
	w.Header().Set("Content-Type", "application/sdp")
	w.Header().Set("Location", fmt.Sprintf("/%s/%s/%s", app, streamKey, resourceId))
	w.WriteHeader(http.StatusCreated)
	w.Write([]byte(sdp))

	return nil
}

func (s *WHIPServer) createStream(streamKey string, sdpOffer string) (string, string, error) {
	ctx, done := context.WithTimeout(s.ctx, sdpResponseTimeout)
	defer done()

	resourceId := utils.NewGuid(utils.WHIPResourcePrefix)

	h, sdpResponse, err := NewWHIPHandler(ctx, s.conf, sdpOffer)
	if err != nil {
		return "", "", err
	}

	s.handlers.Store(resourceId, h)

	var ready func(mimeTypes map[types.StreamKind]string, err error)
	if s.onPublish != nil {
		ready, err = s.onPublish(streamKey, resourceId)
		if err != nil {
			return "", "", err
		}
	}

	go func() {
		ctx, done := context.WithTimeout(s.ctx, sessionStartTimeout)
		defer done()

		var err error
		var mimeTypes map[types.StreamKind]string
		if ready != nil {
			defer func() {
				ready(mimeTypes, err)
			}()
		}

		mimeTypes, err = h.WaitForTracksReady(ctx)
		if err != nil {
			return
		}

		go func() {
			err := h.WaitForSessionEnd(s.ctx)
			if err != nil {
				logger.Warnw("whil session failed", err, "streamKey", streamKey, "resourceID", resourceId)
				// The handler process should update the ingress info
			}

			s.handlers.Delete(resourceId)
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
