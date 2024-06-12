// Copyright 2023 LiveKit, Inc.
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
	"net"
	"os"
	"os/exec"
	"path"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/ipc"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

const (
	network    = "unix"
	maxRetries = 3
)

type process struct {
	sessionCloser

	info       *livekit.IngressInfo
	retryCount int
	cmd        *exec.Cmd
	grpcClient ipc.IngressHandlerClient
	closed     core.Fuse
}

type ProcessManager struct {
	sm     *SessionManager
	newCmd func(ctx context.Context, p *params.Params) (*exec.Cmd, error)

	mu             sync.RWMutex
	activeHandlers map[string]*process
	onFatal        func(info *livekit.IngressInfo, err error)
}

func NewProcessManager(sm *SessionManager, newCmd func(ctx context.Context, p *params.Params) (*exec.Cmd, error)) *ProcessManager {
	return &ProcessManager{
		sm:             sm,
		newCmd:         newCmd,
		activeHandlers: make(map[string]*process),
	}
}

func (s *ProcessManager) onFatalError(f func(info *livekit.IngressInfo, err error)) {
	s.onFatal = f
}

func (s *ProcessManager) startIngress(ctx context.Context, p *params.Params, closeSession func(ctx context.Context)) error {
	// TODO send update on failure
	_, span := tracer.Start(ctx, "Service.startIngress")
	defer span.End()

	h := &process{
		info: p.IngressInfo,
		sessionCloser: func(ctx context.Context) {
			if closeSession != nil {
				closeSession(ctx)
			}

			go func() {
				time.Sleep(time.Second)

				s.mu.Lock()
				h := s.activeHandlers[p.State.ResourceId]
				s.mu.Unlock()

				if h != nil && !h.closed.IsBroken() && h.cmd != nil {
					logger.Infow("killing handler process still present after termination was requested", "ingressID", h.info.IngressId, "resourceID", p.State.ResourceId, "startedAt", p.State.StartedAt)
					if err := h.cmd.Process.Signal(syscall.SIGKILL); err != nil {
						logger.Infow("failed to kill process", "error", err, "ingressID", h.info.IngressId)
					}
				}
			}()
		},
	}

	s.sm.IngressStarted(p.IngressInfo, h)

	s.mu.Lock()
	s.activeHandlers[p.State.ResourceId] = h
	s.mu.Unlock()

	if p.TmpDir != "" {
		err := os.MkdirAll(p.TmpDir, 0755)
		if err != nil {
			logger.Errorw("failed creating halder temp directory", err, "path", p.TmpDir)
			return err
		}
	}

	go s.runHandler(ctx, h, p)

	return nil
}

func (s *ProcessManager) runHandler(ctx context.Context, h *process, p *params.Params) {
	_, span := tracer.Start(ctx, "Service.runHandler")
	defer span.End()

	defer func() {
		h.closed.Break()
		s.sm.IngressEnded(h.info.State.ResourceId)

		if p.TmpDir != "" {
			os.RemoveAll(p.TmpDir)
		}

		s.mu.Lock()
		delete(s.activeHandlers, h.info.State.ResourceId)
		s.mu.Unlock()
	}()

	for h.retryCount = 0; h.retryCount < maxRetries; h.retryCount++ {
		socketAddr := getSocketAddress(p.TmpDir)
		os.Remove(socketAddr)
		conn, err := grpc.Dial(socketAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(_ context.Context, addr string) (net.Conn, error) {
				return net.Dial(network, addr)
			}),
		)
		if err != nil {
			span.RecordError(err)
			logger.Errorw("could not dial grpc handler", err)
		}
		h.grpcClient = ipc.NewIngressHandlerClient(conn)

		cmd, err := s.newCmd(ctx, p)
		if err != nil {
			span.RecordError(err)
			return
		}
		h.cmd = cmd

		var exitErr *exec.ExitError

		err = h.cmd.Run()
		switch {
		case err == nil:
			// success
			return
		case errors.As(err, &exitErr):
			if exitErr.ProcessState.ExitCode() == 1 {
				logger.Infow("relaunching handler process after retryable failure")
			} else if err.Error() == "signal: killed" {
				logger.Infow("handler killed")
				return
			} else {
				logger.Errorw("unknown handler exit code", err)
				return
			}
		default:
			logger.Errorw("could not launch handler", err)
			if s.onFatal != nil {
				s.onFatal(h.info, err)
			}
			return
		}
	}

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
		Timeout:     int32(timeout),
		Debug:       int32(debug),
	}

	resp, err := p.grpcClient.GetPProf(ctx, req)
	if err != nil {
		return nil, err
	}

	return resp.PprofFile, nil
}

func (p *process) GetPipelineDot(ctx context.Context) (string, error) {
	req := &ipc.GstPipelineDebugDotRequest{}

	resp, err := p.grpcClient.GetPipelineDot(ctx, req)
	if err != nil {
		return "", err
	}

	return resp.DotFile, nil
}

func (p *process) UpdateMediaStats(ctx context.Context, s *ipc.MediaStats) error {
	req := &ipc.UpdateMediaStatsRequest{
		Stats: s,
	}

	_, err := p.grpcClient.UpdateMediaStats(ctx, req)

	return err
}

func (p *process) GatherStats(ctx context.Context) (*ipc.MediaStats, error) {
	req := &ipc.GatherMediaStatsRequest{}

	s, err := p.grpcClient.GatherMediaStats(ctx, req)
	if err != nil {
		return nil, err
	}

	return s.Stats, nil
}

func getSocketAddress(handlerTmpDir string) string {
	return path.Join(handlerTmpDir, "service_rpc.sock")
}
