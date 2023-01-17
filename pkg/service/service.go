package service

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/media"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/version"
	"github.com/livekit/livekit-server/pkg/service/rpc"
	"github.com/livekit/protocol/ingress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

const shutdownTimer = time.Second * 5

type Service struct {
	conf    *config.Config
	monitor *stats.Monitor
	manager *ProcessManager

	rpcServer   ingress.RPCServer
	psrpcClient rpc.IOInfoClient

	promServer *http.Server

	rtmpPublishRequests chan rtmpPublishRequest
	shutdown            chan struct{}
}

func NewService(conf *config.Config, psrpcClient rpc.IOInfoClient, rpcServer ingress.RPCServer) *Service {
	monitor := stats.NewMonitor()

	s := &Service{
		conf:                conf,
		monitor:             monitor,
		manager:             NewProcessManager(conf, monitor),
		rpcServer:           rpcServer,
		psrpcClient:         psrpcClient,
		rtmpPublishRequests: make(chan rtmpPublishRequest),
		shutdown:            make(chan struct{}),
	}

	s.manager.onFatalError(func() { s.Stop(false) })

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
	r := rtmpPublishRequest{
		streamKey: streamKey,
		result:    res,
	}

	select {
	case <-s.shutdown:
		return fmt.Errorf("server shutting down")
	case s.rtmpPublishRequests <- r:
		err := <-res
		return err
	}
}

func (s *Service) handleNewRTMPPublisher(ctx context.Context, streamKey string) (*livekit.IngressInfo, int, error) {
	version, resp, err := s.getIngressInfo(ctx, &livekit.GetIngressInfoRequest{
		StreamKey: streamKey,
	})
	if err != nil {
		return nil, 0, err
	}

	err = media.Validate(ctx, resp.Info)
	if err != nil {
		return resp.Info, version, err
	}

	// check cpu load
	if !s.monitor.AcceptIngress(resp.Info) {
		logger.Debugw("rejecting ingress")
		return nil, version, errors.ErrServerCapacityExceeded
	}

	resp.Info.State = &livekit.IngressState{
		Status:    livekit.IngressState_ENDPOINT_BUFFERING,
		StartedAt: time.Now().UnixNano(),
	}

	go s.manager.launchHandler(ctx, resp, version)

	return resp.Info, version, nil
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
		case <-s.shutdown:
			logger.Infow("shutting down")
			for !s.manager.isIdle() {
				time.Sleep(shutdownTimer)
			}
			return nil
		case req := <-s.rtmpPublishRequests:
			go func() {
				ctx, span := tracer.Start(context.Background(), "Service.HandleRequest")
				info, version, err := s.handleNewRTMPPublisher(ctx, req.streamKey)
				if info != nil {
					s.sendUpdate(ctx, info, version, err)
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

func (s *Service) getIngressInfo(ctx context.Context, req *livekit.GetIngressInfoRequest) (int, *livekit.GetIngressInfoResponse, error) {
	race := rpc.NewRace[livekit.GetIngressInfoResponse](ctx)
	race.Go(func(ctx context.Context) (*livekit.GetIngressInfoResponse, error) {
		return s.rpcServer.SendGetIngressInfoRequest(ctx, req)
	})
	race.Go(func(ctx context.Context) (*livekit.GetIngressInfoResponse, error) {
		return s.psrpcClient.GetIngressInfo(ctx, req)
	})
	return race.Wait()
}

func (s *Service) sendUpdate(ctx context.Context, info *livekit.IngressInfo, version int, err error) {
	if err != nil {
		info.State.Status = livekit.IngressState_ENDPOINT_ERROR
		info.State.Error = err.Error()
		logger.Errorw("ingress failed", errors.New(info.State.Error))
	}

	if version == 0 {
		if err := s.rpcServer.SendUpdate(ctx, info.IngressId, info.State); err != nil {
			logger.Errorw("failed to send update", err)
		}
	} else {
		_, err = s.psrpcClient.UpdateIngressState(ctx, &livekit.UpdateIngressStateRequest{
			IngressId: info.IngressId,
			State:     info.State,
		})
		if err != nil {
			logger.Errorw("failed to send update", err)
		}
	}
}

func (s *Service) CanAccept() bool {
	return s.monitor.CanAcceptIngress()
}

func (s *Service) Stop(kill bool) {
	select {
	case <-s.shutdown:
	default:
		close(s.shutdown)
	}

	if s.monitor != nil {
		s.monitor.Stop()
	}

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
