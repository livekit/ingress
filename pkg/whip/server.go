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
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
	"github.com/livekit/protocol/livekit"
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

	conf         *config.Config
	webRTCConfig *rtcconfig.WebRTCConfig
	onPublish    func(streamKey, resourceId string) (*livekit.IngressInfo, func(mimeTypes map[types.StreamKind]string, err error), *params.Params, error)
	handlers     sync.Map
	rpcClient    rpc.IngressHandlerClient
}

func NewWHIPServer(rpcClient rpc.IngressHandlerClient) *WHIPServer {
	return &WHIPServer{
		rpcClient: rpcClient,
	}
}

func (s *WHIPServer) Start(
	conf *config.Config,
	onPublish func(streamKey, resourceId string) (*livekit.IngressInfo, func(mimeTypes map[types.StreamKind]string, err error), *params.Params, error),
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
		resourceID := vars["resource_id"]

		logger.Infow("handling WHIP delete request", "resourceID", resourceID)

		req := &rpc.DeleteWHIPResourceRequest{
			ResourceId: resourceID,
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

func (s *WHIPServer) handleError(err error, w http.ResponseWriter) {
	var psrpcErr psrpc.Error
	switch {
	case errors.As(err, &psrpcErr):
		w.WriteHeader(psrpcErr.ToHttp())
		w.Write([]byte(psrpcErr.Error()))
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

	logger.Debugw("new whip request", "streamKey", streamKey, "sdpOffer", string(sdpOffer.Bytes()))

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

	info, ready, p, err := s.onPublish(streamKey, resourceId)
	if err != nil {
		return "", "", err
	}

	ctx = context.WithValue(ctx, "ingressID", info.IngressId)
	ctx = context.WithValue(ctx, "resourceID", resourceId)

	h, sdpResponse, err := NewWHIPHandler(ctx, s.conf, s.webRTCConfig, p, sdpOffer)
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
				ready(mimeTypes, err)
				if err != nil {
					s.handlers.Delete(resourceId)
				}
			}()
		}

		s.handlers.Store(resourceId, h)

		mimeTypes, err = h.Start(ctx)
		if err != nil {
			return
		}

		logger.Infow("all tracks ready")

		go func() {
			defer func() {
				s.handlers.Delete(resourceId)
			}()

			err := h.WaitForSessionEnd(s.ctx)
			if err != nil {
				logger.Warnw("WHIP session failed", err, "streamKey", streamKey, "resourceID", resourceId)
				// The handler process should update the ingress info
			}
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
