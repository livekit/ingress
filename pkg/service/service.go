package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/media"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/protocol/ingress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

const shutdownTimer = time.Second * 30

type Service struct {
	conf    *config.Config
	monitor *stats.Monitor

	rpcServer ingress.RPCServer

	promServer *http.Server

	processes           sync.Map
	rtmpPublishRequests chan rtmpPublishRequest
	shutdown            chan struct{}
}

type process struct {
	info *livekit.IngressInfo
	cmd  *exec.Cmd
}

type rtmpPublishRequest struct {
	streamKey string
	result    chan<- error
}

func NewService(conf *config.Config, rpcServer ingress.RPCServer) *Service {
	s := &Service{
		conf:                conf,
		monitor:             stats.NewMonitor(),
		rpcServer:           rpcServer,
		rtmpPublishRequests: make(chan rtmpPublishRequest),
		shutdown:            make(chan struct{}),
	}

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
	req := &livekit.GetIngressInfoRequest{
		StreamKey: streamKey,
	}
	resp, err := s.rpcServer.SendGetIngressInfoRequest(ctx, req)
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

	go s.launchHandler(ctx, resp)

	return resp.Info, nil
}

func (s *Service) Run() error {
	logger.Debugw("starting service")

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
			for !s.isIdle() {
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

func (s *Service) isIdle() bool {
	idle := true
	s.processes.Range(func(key, value interface{}) bool {
		idle = false
		return false
	})
	return idle
}

func (s *Service) sendUpdate(ctx context.Context, info *livekit.IngressInfo, err error) {
	if err != nil {
		info.State.Status = livekit.IngressState_ENDPOINT_ERROR
		info.State.Error = err.Error()
		logger.Errorw("ingress failed", errors.New(info.State.Error))
	}

	if err := s.rpcServer.SendUpdate(ctx, info.IngressId, info.State); err != nil {
		logger.Errorw("failed to send update", err)
	}
}

func (s *Service) launchHandler(ctx context.Context, resp *livekit.GetIngressInfoResponse) {
	// TODO send update on failure
	ctx, span := tracer.Start(ctx, "Service.launchHandler")
	defer span.End()

	confString, err := yaml.Marshal(s.conf)
	if err != nil {
		span.RecordError(err)
		logger.Errorw("could not marshal config", err)
		return
	}

	infoString, err := protojson.Marshal(resp.Info)
	if err != nil {
		span.RecordError(err)
		logger.Errorw("could not marshal request", err)
		return
	}

	args := []string{
		"run-handler",
		"--config-body", string(confString),
		"--info", string(infoString),
	}

	if resp.WsUrl != "" {
		args = append(args, "--ws-url", resp.WsUrl)
	}
	if resp.Token != "" {
		args = append(args, "--token", resp.Token)
	}

	cmd := exec.Command("ingress",
		args...,
	)

	cmd.Dir = "/"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	s.monitor.IngressStarted(resp.Info)
	s.processes.Store(resp.Info.IngressId, &process{
		info: resp.Info,
		cmd:  cmd,
	})
	defer func() {
		s.monitor.IngressEnded(resp.Info)
		s.processes.Delete(resp.Info.IngressId)
	}()

	err = cmd.Run()
	if err != nil {
		logger.Errorw("could not launch handler", err)
	}
}

func (s *Service) Status() ([]byte, bool, error) {
	info := map[string]interface{}{
		"CpuLoad": s.monitor.GetCPULoad(),
	}
	s.processes.Range(func(key, value interface{}) bool {
		p := value.(*process)
		info[key.(string)] = p.info
		return true
	})

	b, err := json.Marshal(info)

	return b, s.monitor.CanAcceptIngress(), err
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
		s.processes.Range(func(key, value interface{}) bool {
			p := value.(*process)
			if err := p.cmd.Process.Kill(); err != nil {
				logger.Errorw("failed to kill process", err, "ingressID", key.(string))
			}
			return true
		})
	}
}

func (s *Service) ListIngress() []string {
	res := make([]string, 0)

	s.processes.Range(func(key, value interface{}) bool {
		res = append(res, key.(string))
		return true
	})

	return res
}
