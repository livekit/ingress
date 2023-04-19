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
	"github.com/livekit/ingress/version"
	"github.com/livekit/protocol/ingress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/tracer"
)

const shutdownTimer = time.Second * 5

type publishRequest struct {
	streamKey   string
	inputType   livekit.IngressInput
	extraParams any
	result      chan<- error
}

type Service struct {
	conf    *config.Config
	monitor *stats.Monitor
	manager *ProcessManager

	psrpcClient rpc.IOInfoClient

	promServer *http.Server

	publishRequests chan publishRequest
	shutdown        core.Fuse
}

func NewService(conf *config.Config, psrpcClient rpc.IOInfoClient) *Service {
	monitor := stats.NewMonitor()

	s := &Service{
		conf:            conf,
		monitor:         monitor,
		manager:         NewProcessManager(conf, monitor),
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
	res := make(chan error)
	r := publishRequest{
		streamKey: streamKey,
		inputType: livekit.IngressInput_RTMP_INPUT,
		result:    res,
	}

	select {
	case <-s.shutdown.Watch():
		return errors.ErrServerShuttingDown
	case s.publishRequests <- r:
		err := <-res
		return err
	}
}

func (s *Service) HandleWHIPPublishRequest(streamKey, resourceId, sdpOffer string) error {
	res := make(chan error)
	r := publishRequest{
		streamKey: streamKey,
		inputType: livekit.IngressInput_WHIP_INPUT,
		extraParams: params.WhipExtraParams{
			ResourceId: resourceId,
			SDPOffer:   sdpOffer,
		},
		result: res,
	}

	select {
	case <-s.shutdown.Watch():
		return errors.ErrServerShuttingDown
	case s.publishRequests <- r:
		err := <-res
		return err
	}
}

func (s *Service) handleNewPublisher(ctx context.Context, streamKey string, inputType livekit.IngressInput, extraParams any) (*livekit.IngressInfo, error) {
	resp, err := s.psrpcClient.GetIngressInfo(ctx, &rpc.GetIngressInfoRequest{
		StreamKey: streamKey,
	})
	if err != nil {
		return nil, err
	}

	err = ingress.Validate(resp.Info)
	if err != nil {
		return resp.Info, err
	}

	if inputType != resp.Info.InputType {
		return nil, errors.ErrInvalidIngressType
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

	go s.manager.launchHandler(ctx, resp, extraParams)

	return resp.Info, nil
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
				info, err := s.handleNewPublisher(ctx, req.streamKey, req.inputType, req.extraParams)
				if info != nil {
					s.sendUpdate(ctx, info, err)
				}
				if err != nil {
					span.RecordError(err)
				}
				// Result channel should be buffered
				req.result <- err
				span.End()
			}()
		}
	}
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
