package service

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"

	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"

	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/protocol/logger"
)

func NewCmd(ctx context.Context, p *params.Params) (*exec.Cmd, error) {
	confString, err := yaml.Marshal(p.Config)
	if err != nil {
		logger.Errorw("could not marshal config", err)
		return nil, err
	}

	infoString, err := protojson.Marshal(p.IngressInfo)
	if err != nil {
		logger.Errorw("could not marshal request", err)
		return nil, err
	}

	extraParamsString := ""
	if p.ExtraParams != nil {
		p, err := json.Marshal(p.ExtraParams)
		if err != nil {
			logger.Errorw("could not marshall extra parameters", err)
			return nil, err
		}
		extraParamsString = string(p)
	}

	loggingFields := ""
	if len(p.LoggingFields) > 0 {
		b, err := json.Marshal(p.LoggingFields)
		if err != nil {
			return nil, err
		}
		loggingFields = string(b)
	}

	args := []string{
		"run-handler",
		"--config-body", string(confString),
		"--info", string(infoString),
		"--relay-token", p.RelayToken,
	}

	if p.WsUrl != "" {
		args = append(args, "--ws-url", p.WsUrl)
	}
	if p.Token != "" {
		args = append(args, "--token", p.Token)
	}
	if extraParamsString != "" {
		args = append(args, "--extra-params", extraParamsString)
	}
	if loggingFields != "" {
		args = append(args, "--logging-fields", loggingFields)
	}

	cmd := exec.Command("ingress",
		args...,
	)

	cmd.Dir = "/"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd, nil
}
