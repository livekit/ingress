package service

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/tracer"
)

type process struct {
	info   *livekit.IngressInfo
	cmd    *exec.Cmd
	closed core.Fuse
}

type rtmpPublishRequest struct {
	streamKey string
	result    chan<- error
}

type ProcessManager struct {
	conf    *config.Config
	monitor *stats.Monitor

	mu             sync.RWMutex
	activeHandlers map[string]*process
	onFatal        func(info *livekit.IngressInfo, err error)
}

func NewProcessManager(conf *config.Config, monitor *stats.Monitor) *ProcessManager {
	return &ProcessManager{
		conf:           conf,
		monitor:        monitor,
		activeHandlers: make(map[string]*process),
	}
}

func (s *ProcessManager) onFatalError(f func(info *livekit.IngressInfo, err error)) {
	s.onFatal = f
}

func (s *ProcessManager) launchHandler(ctx context.Context, resp *rpc.GetIngressInfoResponse) {
	// TODO send update on failure
	_, span := tracer.Start(ctx, "Service.launchHandler")
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
	h := &process{
		info:   resp.Info,
		cmd:    cmd,
		closed: core.NewFuse(),
	}

	s.mu.Lock()
	s.activeHandlers[resp.Info.IngressId] = h
	s.mu.Unlock()

	go s.awaitCleanup(h)
}

func (s *ProcessManager) awaitCleanup(h *process) {
	if err := h.cmd.Run(); err != nil {
		logger.Errorw("could not launch handler", err)
		if s.onFatal != nil {
			s.onFatal(h.info, err)
		}
	}

	h.closed.Break()
	s.monitor.IngressEnded(h.info)

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.activeHandlers, h.info.IngressId)
}

func (s *ProcessManager) isIdle() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.activeHandlers) == 0
}

func (s *ProcessManager) listIngress() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ingressIDs := make([]string, 0, len(s.activeHandlers))
	for ingressID := range s.activeHandlers {
		ingressIDs = append(ingressIDs, ingressID)
	}
	return ingressIDs
}

func (s *ProcessManager) killAll() {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, h := range s.activeHandlers {
		if !h.closed.IsBroken() {
			if err := h.cmd.Process.Signal(syscall.SIGINT); err != nil {
				logger.Errorw("failed to kill process", err, "ingressID", h.info.IngressId)
			}
		}
	}
}
