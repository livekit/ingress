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
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/tracer"
	"github.com/livekit/psrpc"
)

const shutdownTimer = time.Second * 5

type publishRequest struct {
	streamKey string
	inputType livekit.IngressInput
	result    chan<- publishResponse
}

type publishResponse struct {
	resp *rpc.GetIngressInfoResponse
	err  error
}

type Service struct {
	conf    *config.Config
	monitor *stats.Monitor
	manager *ProcessManager
	whipSrv *whip.WHIPServer

	psrpcClient rpc.IOInfoClient

	promServer *http.Server

	publishRequests chan publishRequest
	shutdown        core.Fuse
}

func NewService(conf *config.Config, psrpcClient rpc.IOInfoClient, bus psrpc.MessageBus, whipSrv *whip.WHIPServer) *Service {
	monitor := stats.NewMonitor()

	s := &Service{
		conf:            conf,
		monitor:         monitor,
		manager:         NewProcessManager(conf, monitor),
		whipSrv:         whipSrv,
		psrpcClient:     psrpcClient,
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

func (s *Service) HandleRTMPPublishRequest(streamKey string) error {
	ctx, span := tracer.Start(context.Background(), "Service.HandleRTMPPublishRequest")
	defer span.End()

	res := make(chan publishResponse)
	r := publishRequest{
		streamKey: streamKey,
		inputType: livekit.IngressInput_RTMP_INPUT,
		result:    res,
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

	go s.manager.launchHandler(ctx, pRes.resp, nil)

	return nil
}

func (s *Service) HandleWHIPPublishRequest(streamKey, resourceId string, ihs rpc.IngressHandlerServer) (p *params.Params, ready func(mimeTypes map[types.StreamKind]string, err error), ended func(err error), err error) {
	res := make(chan publishResponse)
	r := publishRequest{
		streamKey: streamKey,
		inputType: livekit.IngressInput_WHIP_INPUT,
		result:    res,
	}

	var pRes publishResponse
	select {
	case <-s.shutdown.Watch():
		return nil, nil, nil, nil, errors.ErrServerShuttingDown
	case s.publishRequests <- r:
		pRes = <-res
		if pRes.err != nil {
			return nil, nil, nil, nil, pRes.err
		}
	}

	extraParams := &params.WhipExtraParams{
		ResourceId: resourceId,
	}

	if p.IngressInfo.BypassTranscoding {
		// RPC is handled in the handler process when transcoding

		rpcServer, err := rpc.NewIngressHandlerServer(conf.NodeID, ihs, bus)
		if err != nil {
			return nil
		}

		err = RegisterIngressRpcHandlers(rpcServer, pRes.resp.Info, extraParams)
		if err != nil {
			return nil, nil, nil, nil, err
		}
	}

	wsUrl := s.conf.WsUrl
	if pRes.resp.WsUrl != "" {
		wsUrl = pRes.resp.WsUrl
	}

	p, err = params.GetParams(context.Background(), s.conf, pRes.resp.Info, wsUrl, pRes.resp.Token, extraParams)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	ready = func(mimeTypes map[types.StreamKind]string, err error) {
		ctx, span := tracer.Start(context.Background(), "Service.HandleWHIPPublishRequest.ready")
		defer span.End()
		if err != nil {
			// Client failed to finalize session start
			s.sendUpdate(ctx, pRes.resp.Info, err)
			if p.IngressInfo.BypassTranscoding {
				DeregisterIngressRpcHandlers(rpcServer, pRes.resp.Info, extraParams)
			}
			span.RecordError(err)
			return
		}

		if p.IngressInfo.BypassTranscoding {
			s.sendUpdate(ctx, pRes.resp.Info, nil)

			s.monitor.IngressStarted(pRes.resp.Info)
		} else {
			extraParams.MimeTypes = mimeTypes

			go s.manager.launchHandler(ctx, pRes.resp, extraParams)
		}
	}

	if p.IngressInfo.BypassTranscoding {
		ended = func(err error) {
			ctx, span := tracer.Start(context.Background(), "Service.HandleWHIPPublishRequest.ended")
			defer span.End()

			p.SetStatus(livekit.IngressState_ENDPOINT_INACTIVE, "")

			s.sendUpdate(ctx, pRes.resp.Info, err)
			s.monitor.IngressEnded(pRes.resp.Info)
			DeregisterIngressRpcHandlers(rpcServer, pRes.resp.Info, extraParams)
		}
	}

	return p, ready, ended, nil
}

func (s *Service) handleNewPublisher(ctx context.Context, streamKey string, inputType livekit.IngressInput) (*rpc.GetIngressInfoResponse, error) {
	resp, err := s.psrpcClient.GetIngressInfo(ctx, &rpc.GetIngressInfoRequest{
		StreamKey: streamKey,
	})
	if err != nil {
		return nil, err
	}

	err = ingress.Validate(resp.Info)
	if err != nil {
		return resp, err
	}

	if inputType != resp.Info.InputType {
		return nil, ingress.ErrInvalidIngressType
	}

	// check cpu load
	if !s.monitor.AcceptIngress(resp.Info) {
		logger.Debugw("rejecting ingress")
		return nil, errors.ErrServerCapacityExceeded
	}

	resp.Info.State = &livekit.IngressState{
		Status:    livekit.IngressState_ENDPOINT_BUFFERING,
		StartedAt: time.Now().UnixNano(),
	}

	return resp, nil
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
			for !s.manager.isIdle() {
				time.Sleep(shutdownTimer)
			}
			return nil
		case req := <-s.publishRequests:
			go func() {
				ctx, span := tracer.Start(context.Background(), "Service.HandleRequest")
				defer span.End()

				resp, err := s.handleNewPublisher(ctx, req.streamKey, req.inputType)
				if resp != nil && resp.Info != nil {
					s.sendUpdate(ctx, resp.Info, err)
				}
				if err != nil {
					span.RecordError(err)
				}
				// Result channel should be buffered
				req.result <- publishResponse{
					resp: resp,
					err:  err,
				}
			}()
		}
	}
}

func (s *Service) isIdle() bool {
	return s.manager.isIdle() && s.whipSrv.IsIdle()
}

func (s *Service) sendUpdate(ctx context.Context, info *livekit.IngressInfo, err error) {
	state := info.State
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
	return !s.shutdown.IsBroken() && s.monitor.CanAcceptIngress()
}

func (s *Service) Stop(kill bool) {
	s.shutdown.Once(func() {
		if s.monitor != nil {
			s.monitor.Stop()
		}
	})

	if kill {
		s.manager.killAll()
	}
}

func (s *Service) ListIngress() []string {
	return s.manager.listIngress()
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
