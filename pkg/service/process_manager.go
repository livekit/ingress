package service

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path"
	"sync"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/ipc"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

type process struct {
	info       *livekit.IngressInfo
	cmd        *exec.Cmd
	grpcClient ipc.IngressHandlerClient
	closed     core.Fuse
}

type ProcessManager struct {
	conf *config.Config
	sm   *SessionManager

	mu             sync.RWMutex
	activeHandlers map[string]*process
	onFatal        func(info *livekit.IngressInfo, err error)
}

func NewProcessManager(conf *config.Config, sm *SessionManager) *ProcessManager {
	return &ProcessManager{
		conf:           conf,
		sm:             sm,
		activeHandlers: make(map[string]*process),
	}
}

func (s *ProcessManager) onFatalError(f func(info *livekit.IngressInfo, err error)) {
	s.onFatal = f
}

func (s *ProcessManager) launchHandler(ctx context.Context, p *params.Params) {
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

	extraParamsString := ""
	if extraParams != nil {
		p, err := json.Marshal(extraParams)
		if err != nil {
			span.RecordError(err)
			logger.Errorw("could not marshall extra parameters", err)
		}
		extraParamsString = string(p)
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
	if extraParamsString != "" {
		args = append(args, "--extra-params", extraParamsString)
	}

	cmd := exec.Command("ingress",
		args...,
	)

	cmd.Dir = "/"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	socketAddr := getSocketAddress(p.TmpDir)
	conn, err := grpc.Dial(socketAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(_ context.Context, addr string) (net.Conn, error) {
			return net.Dial(network, addr)
		}),
	)
	if err != nil {
		span.RecordError(err)
		logger.Errorw("could not dial grpc handler", err)
		return err
	}
	h.grpcClient = ipc.NewIngressHandlerClient(conn)

	h := &process{
		info:   resp.Info,
		cmd:    cmd,
		closed: core.NewFuse(),
	}
	s.sm.IngressStarted(resp.Info)

	s.mu.Lock()
	s.activeHandlers[resp.Info.State.ResourceId] = h
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
	s.sm.IngressEnded(h.info)

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.activeHandlers, h.info.State.ResourceId)
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

func (p *process) GetProfileData(ctx context.Context, profileName string, timeout int, debug int) (b []byte, err error) {
	req := &ipc.PProfRequest{
		ProfileName: profileName,
		Timeout:     timeout,
		Debug:       debuf,
	}

	resp, err := p.grpcClient.GetPProf(ctx, req)
	if err != nil {
		return nil, err
	}

	return resp.PprofFile, nil
}

func getSocketAddress(handlerTmpDir string) string {
	return path.Join(handlerTmpDir, "service_rpc.sock")
}
