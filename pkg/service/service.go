// Copyright 2023-2025 LiveKit, Inc.
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

package service

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/frostbyte73/core"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/ipc"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/rtmp"
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
	"github.com/livekit/protocol/utils"
	"github.com/livekit/psrpc"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

const shutdownTimer = time.Second * 5

type publishResponse struct {
	params *params.Params
	err    error
}

type Service struct {
	confLock sync.Mutex
	conf     *config.Config

	monitor *stats.Monitor
	manager *ProcessManager
	sm      *SessionManager
	whipSrv *whip.WHIPServer
	rtmpSrv *rtmp.RTMPServer

	psrpcClient rpc.IOInfoClient
	rpcSrv      rpc.IngressInternalServer
	bus         psrpc.MessageBus

	promServer *http.Server

	isActive atomic.Bool
	shutdown core.Fuse
}

func NewService(conf *config.Config, psrpcClient rpc.IOInfoClient, bus psrpc.MessageBus, rtmpSrv *rtmp.RTMPServer, whipSrv *whip.WHIPServer, newCmd func(ctx context.Context, p *params.Params) (*exec.Cmd, error), listIngressTopic string) (*Service, error) {
	monitor := stats.NewMonitor()

	s := &Service{
		conf:        conf,
		monitor:     monitor,
		whipSrv:     whipSrv,
		rtmpSrv:     rtmpSrv,
		psrpcClient: psrpcClient,
		bus:         bus,
	}

	srv, err := rpc.NewIngressInternalServer(s, bus)
	if err != nil {
		return nil, err
	}

	err = srv.RegisterListActiveIngressTopic(listIngressTopic)
	if err != nil {
		return nil, err
	}
	s.rpcSrv = srv

	s.sm = NewSessionManager(monitor, srv)

	s.manager = NewProcessManager(s.sm, newCmd)
	s.isActive.Store(true)

	s.manager.onFatalError(func(info *livekit.IngressInfo, err error) {
		s.sendUpdate(context.Background(), info, err)

		s.Stop(false)
	})

	if conf.PrometheusPort > 0 {
		s.promServer = &http.Server{
			Addr:    fmt.Sprintf(":%d", conf.PrometheusPort),
			Handler: promhttp.Handler(),
		}

		// Register default Prometheus collectors only when Prometheus is enabled
		if err := prometheus.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})); err != nil {
			if _, ok := err.(prometheus.AlreadyRegisteredError); !ok {
				logger.Errorw("failed to register process collector", err)
			}
		}

		// Unregister the default Go collector before registering detailed runtime metrics
		prometheus.Unregister(prometheus.NewGoCollector())
		if err := prometheus.Register(collectors.NewGoCollector(collectors.WithGoCollectorRuntimeMetrics(collectors.MetricsAll))); err != nil {
			if _, ok := err.(prometheus.AlreadyRegisteredError); !ok {
				logger.Errorw("failed to register go collector", err)
			}
		}

		if err := prometheus.Register(collectors.NewBuildInfoCollector()); err != nil {
			if _, ok := err.(prometheus.AlreadyRegisteredError); !ok {
				logger.Errorw("failed to register build info collector", err)
			}
		}
	}

	return s, nil
}

func (s *Service) HandleRTMPPublishRequest(streamKey, resourceId string) (*params.Params, *stats.LocalMediaStatsGatherer, error) {
	ctx, span := tracer.Start(context.Background(), "Service.HandleRTMPPublishRequest")
	defer span.End()

	p, err := s.handleRequest(ctx, streamKey, resourceId, livekit.IngressInput_RTMP_INPUT, nil, "", "", nil, nil)
	if err != nil {
		return nil, nil, err
	}

	err = s.manager.startIngress(ctx, p, func(ctx context.Context) {
		s.rtmpSrv.CloseHandler(resourceId)
	})
	if err != nil {
		return nil, nil, err
	}

	stats, err := s.sm.GetIngressMediaStats(resourceId)
	if err != nil {
		return nil, nil, err
	}

	return p, stats, nil
}

func (s *Service) HandleWHIPPublishRequest(streamKey, resourceId string) (p *params.Params, ready func(mimeTypes map[types.StreamKind]string, err error) *stats.LocalMediaStatsGatherer, ended func(err error), err error) {
	ctx, span := tracer.Start(context.Background(), "Service.HandleWHIPPublishRequest")
	defer span.End()

	p, err = s.handleRequest(ctx, streamKey, resourceId, livekit.IngressInput_WHIP_INPUT, nil, "", "", nil, nil)
	if err != nil {
		return nil, nil, nil, err
	}

	ready = func(mimeTypes map[types.StreamKind]string, err error) *stats.LocalMediaStatsGatherer {
		ctx, span := tracer.Start(context.Background(), "Service.HandleWHIPPublishRequest.ready")
		defer span.End()
		if err != nil {
			// Client failed to finalize session start
			logger.Warnw("ingress failed", err)
			p.SetStatus(livekit.IngressState_ENDPOINT_ERROR, err)
			p.SendStateUpdate(ctx)

			span.RecordError(err)
			return nil
		}

		if !*p.EnableTranscoding {
			p.SetStatus(livekit.IngressState_ENDPOINT_PUBLISHING, nil)
			p.SendStateUpdate(ctx)

			s.sm.IngressStarted(p.IngressInfo, &localSessionAPI{stats.LocalStatsUpdater{Params: p}, func(ctx context.Context) {
				s.whipSrv.CloseHandler(resourceId)
			}})
		} else {
			p.SetExtraParams(&params.WhipExtraParams{
				MimeTypes: mimeTypes,
			})

			err := s.manager.startIngress(ctx, p, func(ctx context.Context) {
				s.whipSrv.CloseHandler(resourceId)
			})
			if err != nil {
				return nil
			}
		}

		// TODO remove non transcoded stats
		stats, err := s.sm.GetIngressMediaStats(resourceId)
		if err != nil {
			return nil
		}
		return stats
	}

	if !*p.EnableTranscoding {
		ended = func(err error) {
			ctx, span := tracer.Start(context.Background(), "Service.HandleWHIPPublishRequest.ended")
			defer span.End()

			if err == nil {
				p.SetStatus(livekit.IngressState_ENDPOINT_INACTIVE, nil)
			} else {
				logger.Warnw("ingress failed", err)
				p.SetStatus(livekit.IngressState_ENDPOINT_ERROR, err)
			}

			p.SendStateUpdate(ctx)
			s.sm.IngressEnded(p.IngressInfo.State.ResourceId)
		}
	}

	return p, ready, ended, nil
}

func (s *Service) HandleURLPublishRequest(ctx context.Context, resourceId string, req *rpc.StartIngressRequest) (*livekit.IngressInfo, error) {
	ctx, span := tracer.Start(ctx, "Service.HandleURLPublishRequest")
	defer span.End()

	p, err := s.handleRequest(ctx, "", resourceId, livekit.IngressInput_URL_INPUT, req.Info, req.WsUrl, req.Token, req.FeatureFlags, req.LoggingFields)
	if err != nil {
		return nil, err
	}

	err = s.manager.startIngress(ctx, p, nil)
	if err != nil {
		return nil, err
	}

	return p.IngressInfo, nil
}

func (s *Service) handleRequest(ctx context.Context, streamKey string, resourceId string, inputType livekit.IngressInput, info *livekit.IngressInfo, wsUrl string, token string, featureFlags map[string]string, loggingFields map[string]string) (p *params.Params, err error) {

	ctx, span := tracer.Start(ctx, "Service.HandleRequest")
	defer span.End()

	if s.shutdown.IsBroken() {
		return nil, errors.ErrServerShuttingDown
	}

	res := make(chan publishResponse)

	go func() {
		var p *params.Params
		var err error

		defer func() {
			if err != nil {
				span.RecordError(err)
			}

			// Result channel should be buffered
			res <- publishResponse{
				params: p,
				err:    err,
			}
		}()

		if info == nil {
			var resp *rpc.GetIngressInfoResponse
			resp, err = s.psrpcClient.GetIngressInfo(ctx, &rpc.GetIngressInfoRequest{
				StreamKey: streamKey,
			})
			if err != nil {
				logger.Infow("failed retrieving ingress info", "streamKey", streamKey, "error", err)
				return
			}

			info = resp.Info
			wsUrl = resp.WsUrl
			token = resp.Token
			featureFlags = resp.FeatureFlags
			loggingFields = resp.LoggingFields
		}

		p, err = s.handleNewPublisher(ctx, resourceId, inputType, info, wsUrl, token, featureFlags, loggingFields)
		if p != nil {
			info = p.IngressInfo
		}

		// Create the ingress if it came through the request (URL Pull)
		if inputType == livekit.IngressInput_URL_INPUT && err == nil {
			_, err = s.psrpcClient.CreateIngress(ctx, info)
			if err != nil {
				logger.Warnw("failed creating ingress", err)
				// TODO remove this workaround once updated IOInfoService that handles CreateIngress is deployed widely
				var psrpcErr psrpc.Error
				if errors.As(err, &psrpcErr) && psrpcErr.Code() == psrpc.Unavailable {
					err = nil
				}
				return
			}
		}

		// Send update for URL ingress as well to make sure the state gets updated even if CreateIngress fails because the ingress already exists
		s.sendUpdate(ctx, info, err)

		if info != nil {
			logger.Infow("received ingress info", "ingressID", info.IngressId, "streamKey", info.StreamKey, "resourceID", info.State.ResourceId, "ingressInfo", params.CopyRedactedIngressInfo(info))
		}
	}()

	select {
	case <-s.shutdown.Watch():
		return nil, errors.ErrServerShuttingDown
	case pub := <-res:
		return pub.params, pub.err
	}
}

func (s *Service) handleNewPublisher(ctx context.Context, resourceId string, inputType livekit.IngressInput, info *livekit.IngressInfo, wsUrl string, token string, featureFlags map[string]string, loggingFields map[string]string) (*params.Params, error) {
	info.State = &livekit.IngressState{
		Status:     livekit.IngressState_ENDPOINT_BUFFERING,
		StartedAt:  time.Now().UnixNano(),
		ResourceId: resourceId,
	}

	if info.Enabled != nil && !*info.Enabled {
		return nil, ingress.ErrIngressDisabled
	}

	s.confLock.Lock()
	conf := s.conf
	s.confLock.Unlock()

	if wsUrl == "" {
		wsUrl = conf.WsUrl
	}

	// This validates the ingress info
	p, err := params.GetParams(ctx, s.psrpcClient, conf, info, wsUrl, token, "", featureFlags, loggingFields, nil)
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

func (s *Service) UpdateConfig(conf *config.Config) {
	s.confLock.Lock()
	defer s.confLock.Unlock()

	s.conf = conf

	err := s.monitor.UpdateCostConfig(&conf.CPUCost)
	if err != nil {
		logger.Errorw("monitor cost config validation failed", err)
	}
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

	s.confLock.Lock()
	conf := s.conf
	s.confLock.Unlock()

	if err := s.monitor.Start(conf); err != nil {
		return err
	}

	logger.Debugw("service ready")

	<-s.shutdown.Watch()
	logger.Infow("shutting down")
	for !s.sm.IsIdle() {
		logger.Debugw("instance waiting for sessions to finish", "sessions_count", len(s.ListIngress()))
		time.Sleep(shutdownTimer)
	}

	if s.monitor != nil {
		s.monitor.Stop()
	}

	return nil
}

func (s *Service) sendUpdate(ctx context.Context, info *livekit.IngressInfo, err error) {
	var state *livekit.IngressState
	if info == nil {
		return
	}
	state = info.State
	if state == nil {
		state = &livekit.IngressState{}
	}
	if err != nil {
		state.Status = livekit.IngressState_ENDPOINT_ERROR
		state.Error = err.Error()
		logger.Warnw("ingress failed", errors.New(state.Error))
	}

	state.UpdatedAt = time.Now().UnixNano()

	_, err = s.psrpcClient.UpdateIngressState(ctx, &rpc.UpdateIngressStateRequest{
		IngressId: info.IngressId,
		State:     state,
	})
	if err != nil {
		logger.Errorw("failed to send update", err)
	}
}

func (s *Service) GetAvailableCPU() float64 {
	return s.monitor.GetAvailableCPU()
}

func (s *Service) CanAccept() bool {
	return s.monitor.CanAccept()
}

func (s *Service) IsActive() bool {
	return s.isActive.Load()
}

func (s *Service) Pause() {
	s.isActive.Store(false)
}

func (s *Service) Resume() {
	s.isActive.Store(true)
}

func (s *Service) Stop(kill bool) {
	s.shutdown.Break()
	s.monitor.Shutdown()

	if kill {
		s.manager.killAll()
	}
}

func (s *Service) ListIngress() []*rpc.IngressSession {
	return s.sm.ListIngress()
}

func (s *Service) ListActiveIngress(ctx context.Context, _ *rpc.ListActiveIngressRequest) (*rpc.ListActiveIngressResponse, error) {
	_, span := tracer.Start(ctx, "Service.ListActiveIngress")
	defer span.End()

	sessions := s.ListIngress()
	ids := make([]string, 0, len(sessions))
	for _, s := range sessions {
		// backward compatiblity
		ids = append(ids, s.IngressId)
	}

	return &rpc.ListActiveIngressResponse{
		IngressIds:      ids,
		IngressSessions: sessions,
	}, nil
}

func (s *Service) StartIngress(ctx context.Context, req *rpc.StartIngressRequest) (*livekit.IngressInfo, error) {
	return s.HandleURLPublishRequest(ctx, utils.NewGuid(utils.URLResourcePrefix), req)
}

func (s *Service) StartIngressAffinity(ctx context.Context, req *rpc.StartIngressRequest) float32 {
	if !s.isActive.Load() || !s.monitor.CanAcceptIngress(req.Info) {
		return -1
	}

	return 1
}

func (s *Service) KillIngressSession(ctx context.Context, req *rpc.KillIngressSessionRequest) (*emptypb.Empty, error) {
	s.sm.IngressEnded(req.Session.ResourceId)

	return &emptypb.Empty{}, nil
}

func (s *Service) GetHealthHandlers() whip.HealthHandlers {
	return whip.HealthHandlers{
		"/health":       http.HandlerFunc(s.HealthHandler),
		"/availability": http.HandlerFunc(s.AvailabilityHandler),
	}
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

func (s *Service) GetWhipProxyEnabled(ctx context.Context, featureFlags map[string]string) bool {
	return s.conf.WHIPProxyEnabled
}

type sessionCloser func(ctx context.Context)

func (p sessionCloser) CloseSession(ctx context.Context) {
	if p != nil {
		p(ctx)
	}
}

type localSessionAPI struct {
	stats.LocalStatsUpdater
	sessionCloser
}

func (a *localSessionAPI) GetProfileData(ctx context.Context, profileName string, timeout int, debug int) (b []byte, err error) {
	return pprof.GetProfileData(ctx, profileName, timeout, debug)
}

func (a *localSessionAPI) GetPipelineDot(ctx context.Context) (string, error) {
	// No dot file if transcoding is disabled
	return "", errors.ErrIngressNotFound
}

func (a *localSessionAPI) GatherStats(ctx context.Context) (*ipc.MediaStats, error) {
	// Return a nil stats map. Use the local gatherer in the session manager for local stats

	return nil, nil
}
