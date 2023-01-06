package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/livekit/cloud/version"
	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/media"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/livekit-server/pkg/service/rpc"
	"github.com/livekit/protocol/ingress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

const shutdownTimer = time.Second * 30

type Service struct {
	conf    *config.Config
	monitor *stats.Monitor
	manager *ProcessManager

	rpcServer   ingress.RPCServer
	psrpcServer ingress.InternalServer

	promServer *http.Server

	processes           sync.Map
	rtmpPublishRequests chan rtmpPublishRequest
	shutdown            chan struct{}
}

func NewService(conf *config.Config, psrpcServer ingress.InternalServer, rpcServer ingress.RPCServer) *Service {
	monitor := stats.NewMonitor()

	s := &Service{
		conf:                conf,
		monitor:             monitor,
		manager:             NewProcessManager(conf, monitor),
		rpcServer:           rpcServer,
		psrpcServer:         psrpcServer,
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

func (s *Service) handleNewRTMPPublisher(ctx context.Context, streamKey string) (*livekit.IngressInfo, error) {
	version, resp, err := s.getIngressInfo(ctx, &livekit.GetIngressInfoRequest{
		StreamKey: streamKey,
	})
	if err != nil {
		return nil, err
	}

	err = media.Validate(ctx, resp.Info)
	if err != nil {
		return resp.Info, err
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

	go s.launchHandler(ctx, resp, version)

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

	if err := s.psrpcServer.SetServerImpl(s); err != nil {
		return err
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
				info, err := s.handleNewRTMPPublisher(ctx, req.streamKey)
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

func (s *Service) getIngressInfo(ctx context.Context, req *livekit.GetIngressInfoRequest) (int, *livekit.GetIngressInfoResponse, error) {
	type result struct {
		version int
		resp    *livekit.GetIngressInfoResponse
		err     error
	}
	var res atomic.Pointer[result]

	// race the legacy/psrpc apis and use whichever returns first
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		defer cancel()
		resp, err := s.rpcServer.SendGetIngressInfoRequest(ctx, req)
		res.CompareAndSwap(nil, &result{0, resp, err})
	}()
	go func() {
		defer cancel()
		resp, err := s.psrpcServer.GetIngressInfo(ctx, req)
		res.CompareAndSwap(nil, &result{1, resp, err})
	}()
	<-ctx.Done()

	if r := res.Load(); r != nil {
		return r.version, r.resp, r.err
	}
	return 0, nil, ctx.Err()
}

func (s *Service) Status() ([]byte, error) {
	return json.Marshal(s.manager.status())
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
	ctx, span := tracer.Start(ctx, "Service.ListActiveIngress")
	defer span.End()

	return &rpc.ListActiveIngressResponse{
		IngressIds: s.ListIngress(),
	}, nil
}
