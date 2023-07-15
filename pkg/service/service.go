package service

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/frostbyte73/core"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/ingress/pkg/whip"
	"github.com/livekit/ingress/version"
	"github.com/livekit/protocol/ingress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/pprof"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/tracer"
	"github.com/livekit/psrpc"
)

const shutdownTimer = time.Second * 5

type publishRequest struct {
	streamKey  string
	resourceId string
	inputType  livekit.IngressInput
	result     chan<- publishResponse
}

type publishResponse struct {
	params *params.Params
	err    error
}

type Service struct {
	conf    *config.Config
	monitor *stats.Monitor
	manager *ProcessManager
	sm      *SessionManager
	whipSrv *whip.WHIPServer

	psrpcClient rpc.IOInfoClient
	bus         psrpc.MessageBus

	promServer *http.Server

	publishRequests chan publishRequest
	shutdown        core.Fuse
}

func NewService(conf *config.Config, psrpcClient rpc.IOInfoClient, bus psrpc.MessageBus, whipSrv *whip.WHIPServer) *Service {
	monitor := stats.NewMonitor()
	sm := NewSessionManager(monitor)

	s := &Service{
		conf:            conf,
		monitor:         monitor,
		sm:              sm,
		manager:         NewProcessManager(conf, sm),
		whipSrv:         whipSrv,
		psrpcClient:     psrpcClient,
		bus:             bus,
		publishRequests: make(chan publishRequest, 5),
		shutdown:        core.NewFuse(),
	}

	s.manager.onFatalError(func(info *livekit.IngressInfo, err error) {
		s.sendUpdate(context.Background(), info, err)

		s.Stop(false)
	})

	if conf.PrometheusPort > 0 {
		s.promServer = &http.Server{
			Addr:    fmt.Sprintf(":%d", conf.PrometheusPort),
			Handler: promhttp.Handler(),
		}
	}

	return s
}

func (s *Service) HandleRTMPPublishRequest(streamKey, resourceId string) error {
	ctx, span := tracer.Start(context.Background(), "Service.HandleRTMPPublishRequest")
	defer span.End()

	res := make(chan publishResponse)
	r := publishRequest{
		streamKey:  streamKey,
		resourceId: resourceId,
		inputType:  livekit.IngressInput_RTMP_INPUT,
		result:     res,
	}

	var pRes publishResponse
	select {
	case <-s.shutdown.Watch():
		return errors.ErrServerShuttingDown
	case s.publishRequests <- r:
		pRes = <-res
		if pRes.err != nil {
			return pRes.err
		}
	}

	go s.manager.launchHandler(ctx, pRes.params)

	return nil
}

func (s *Service) HandleWHIPPublishRequest(streamKey, resourceId string, ihs rpc.IngressHandlerServerImpl) (p *params.Params, ready func(mimeTypes map[types.StreamKind]string, err error), ended func(err error), err error) {
	res := make(chan publishResponse)
	r := publishRequest{
		streamKey:  streamKey,
		resourceId: resourceId,
		inputType:  livekit.IngressInput_WHIP_INPUT,
		result:     res,
	}

	var pRes publishResponse
	select {
	case <-s.shutdown.Watch():
		return nil, nil, nil, errors.ErrServerShuttingDown
	case s.publishRequests <- r:
		pRes = <-res
		if pRes.err != nil {
			return nil, nil, nil, pRes.err
		}
	}

	var rpcServer rpc.IngressHandlerServer
	if pRes.params.BypassTranscoding {
		// RPC is handled in the handler process when transcoding

		rpcServer, err = rpc.NewIngressHandlerServer(s.conf.NodeID, ihs, s.bus)
		if err != nil {
			return nil, nil, nil, err
		}

		err = RegisterIngressRpcHandlers(rpcServer, pRes.params.IngressInfo)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	ready = func(mimeTypes map[types.StreamKind]string, err error) {
		ctx, span := tracer.Start(context.Background(), "Service.HandleWHIPPublishRequest.ready")
		defer span.End()
		if err != nil {
			// Client failed to finalize session start
			logger.Warnw("ingress failed", err)
			pRes.params.SetStatus(livekit.IngressState_ENDPOINT_ERROR, err.Error())
			pRes.params.SendStateUpdate(ctx)

			if pRes.params.BypassTranscoding {
				DeregisterIngressRpcHandlers(rpcServer, pRes.params.IngressInfo)
			}
			span.RecordError(err)
			return
		}

		if pRes.params.BypassTranscoding {
			pRes.params.SetStatus(livekit.IngressState_ENDPOINT_PUBLISHING, "")
			pRes.params.SendStateUpdate(ctx)

			s.sm.IngressStarted(pRes.params.IngressInfo, GetProfileDataFunc(pprof.GetProfileData))
		} else {
			pRes.params.SetExtraParams(&params.WhipExtraParams{
				MimeTypes: mimeTypes,
			})

			go s.manager.launchHandler(ctx, pRes.params)
		}
	}

	if pRes.params.BypassTranscoding {
		ended = func(err error) {
			ctx, span := tracer.Start(context.Background(), "Service.HandleWHIPPublishRequest.ended")
			defer span.End()

			if err == nil {
				pRes.params.SetStatus(livekit.IngressState_ENDPOINT_INACTIVE, "")
			} else {
				logger.Warnw("ingress failed", err)
				pRes.params.SetStatus(livekit.IngressState_ENDPOINT_ERROR, err.Error())
			}

			pRes.params.SendStateUpdate(ctx)
			s.sm.IngressEnded(pRes.params.IngressInfo)
			DeregisterIngressRpcHandlers(rpcServer, pRes.params.IngressInfo)
		}
	}

	return pRes.params, ready, ended, nil
}

func (s *Service) handleNewPublisher(ctx context.Context, streamKey string, resourceId string, inputType livekit.IngressInput) (*params.Params, error) {
	resp, err := s.psrpcClient.GetIngressInfo(ctx, &rpc.GetIngressInfoRequest{
		StreamKey: streamKey,
	})
	if err != nil {
		return nil, err
	}

	resp.Info.State = &livekit.IngressState{
		Status:     livekit.IngressState_ENDPOINT_BUFFERING,
		StartedAt:  time.Now().UnixNano(),
		ResourceId: resourceId,
	}

	wsUrl := s.conf.WsUrl
	if resp.WsUrl != "" {
		wsUrl = resp.WsUrl
	}
	// This validates the ingress info
	p, err := params.GetParams(ctx, s.psrpcClient, s.conf, resp.Info, wsUrl, resp.Token, nil)
	if err != nil {
		return nil, err
	}

	if inputType != p.InputType {
		return nil, ingress.ErrInvalidIngressType
	}

	// check cpu load
	if !s.monitor.AcceptIngress(p.IngressInfo) {
		logger.Debugw("rejecting ingress")
		return nil, errors.ErrServerCapacityExceeded
	}

	return p, nil
}

func (s *Service) Run() error {
	logger.Debugw("starting service", "version", version.Version)

	if s.promServer != nil {
		promListener, err := net.Listen("tcp", s.promServer.Addr)
		if err != nil {
			return err
		}
		go func() {
			_ = s.promServer.Serve(promListener)
		}()
	}

	if err := s.monitor.Start(s.conf); err != nil {
		return err
	}

	logger.Debugw("service ready")

	for {
		select {
		case <-s.shutdown.Watch():
			logger.Infow("shutting down")
			for !s.sm.IsIdle() {
				logger.Debugw("instance waiting for sessions to finish", "sessions_count", len(s.ListIngress()))
				time.Sleep(shutdownTimer)
			}

			if s.monitor != nil {
				s.monitor.Stop()
			}

			return nil
		case req := <-s.publishRequests:
			go func() {
				ctx, span := tracer.Start(context.Background(), "Service.HandleRequest")
				defer span.End()

				p, err := s.handleNewPublisher(ctx, req.streamKey, req.resourceId, req.inputType)
				var info *livekit.IngressInfo
				if p != nil {
					info = p.IngressInfo
				}
				s.sendUpdate(ctx, info, err)

				if err != nil {
					span.RecordError(err)
				} else {
					logger.Infow("received ingress info", "ingressID", resp.Info.IngressId, "streamKey", resp.Info.StreamKey, "resourceID", resp.Info.State.ResourceId, "ingressInfo", params.CopyRedactedIngressInfo(resp.Info))
				}
				// Result channel should be buffered
				req.result <- publishResponse{
					params: p,
					err:    err,
				}
			}()
		}
	}
}

func (s *Service) sendUpdate(ctx context.Context, info *livekit.IngressInfo, err error) {
	var state *livekit.IngressState
	if info != nil {
		state = info.State
	}
	if state == nil {
		state = &livekit.IngressState{}
	}
	if err != nil {
		state.Status = livekit.IngressState_ENDPOINT_ERROR
		state.Error = err.Error()
		logger.Warnw("ingress failed", errors.New(state.Error))
	}

	_, err = s.psrpcClient.UpdateIngressState(ctx, &rpc.UpdateIngressStateRequest{
		IngressId: info.IngressId,
		State:     state,
	})
	if err != nil {
		logger.Errorw("failed to send update", err)
	}
}

func (s *Service) CanAccept() bool {
	return s.monitor.CanAcceptIngress()
}

func (s *Service) Stop(kill bool) {
	s.shutdown.Break()
	s.monitor.Shutdown()

	if kill {
		s.manager.killAll()
	}
}

func (s *Service) ListIngress() []string {
	return s.sm.ListIngress()
}

func (s *Service) ListActiveIngress(ctx context.Context, _ *rpc.ListActiveIngressRequest) (*rpc.ListActiveIngressResponse, error) {
	_, span := tracer.Start(ctx, "Service.ListActiveIngress")
	defer span.End()

	return &rpc.ListActiveIngressResponse{
		IngressIds: s.ListIngress(),
	}, nil
}

func (s *Service) AvailabilityHandler(w http.ResponseWriter, r *http.Request) {
	if !s.CanAccept() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("No availability"))
	}

	_, _ = w.Write([]byte("Available"))
}

func (s *Service) HealthHandler(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("Healthy"))
}

func RegisterIngressRpcHandlers(server rpc.IngressHandlerServer, info *livekit.IngressInfo) error {
	if err := server.RegisterUpdateIngressTopic(info.IngressId); err != nil {
		return err
	}
	if err := server.RegisterDeleteIngressTopic(info.IngressId); err != nil {
		return err
	}

	if info.InputType == livekit.IngressInput_WHIP_INPUT {
		if err := server.RegisterDeleteWHIPResourceTopic(info.State.ResourceId); err != nil {
			return err
		}
	}

	return nil
}

func DeregisterIngressRpcHandlers(server rpc.IngressHandlerServer, info *livekit.IngressInfo) {
	server.DeregisterUpdateIngressTopic(info.IngressId)
	server.RegisterDeleteIngressTopic(info.IngressId)

	if info.InputType == livekit.IngressInput_WHIP_INPUT {
		server.RegisterDeleteWHIPResourceTopic(info.State.ResourceId)
	}
}
