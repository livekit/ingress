package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

func (s *Service) sendUpdate(ctx context.Context, info *livekit.IngressInfo, err error) {
	if err != nil {
		info.State.Status = livekit.IngressState_ENDPOINT_ERROR
		info.State.Error = err.Error()
		logger.Errorw("ingress failed", errors.New(info.State.Error))
	}

	if err := s.rpcServer.SendUpdate(ctx, info.IngressId, info.State); err != nil {
		logger.Errorw("failed to send update", err)
	}

	err = s.psrpcServer.PublishStateUpdate(ctx, &livekit.UpdateIngressStateRequest{
		IngressId: info.IngressId,
		State:     info.State,
	})
	if err != nil {
		logger.Errorw("failed to send update", err)
	}
}

func (s *Service) launchHandler(ctx context.Context, resp *livekit.GetIngressInfoResponse, version int) {
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
		"--version", fmt.Sprint(version),
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
