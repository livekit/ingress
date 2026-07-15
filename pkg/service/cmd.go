// Copyright 2024 LiveKit, Inc.
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
	"encoding/json"
	"os"
	"os/exec"

	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"

	"github.com/livekit/protocol/logger"

	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/utils"
)

func NewCmd(_ context.Context, p *params.Params) (*exec.Cmd, error) {
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

	featureFlags := ""
	if len(p.FeatureFlags) > 0 {
		b, err := json.Marshal(p.FeatureFlags)
		if err != nil {
			return nil, err
		}
		featureFlags = string(b)
	}

	loggingFields := ""
	if len(p.LoggingFields) > 0 {
		b, err := json.Marshal(p.LoggingFields)
		if err != nil {
			return nil, err
		}
		loggingFields = string(b)
	}

	cmd := exec.Command("ingress", "run-handler")
	cmd.Dir = "/"
	cmd.Env = append(os.Environ(),
		"INGRESS_HANDLER_CONFIG_BODY="+string(confString),
		"INGRESS_HANDLER_INFO="+string(infoString),
		"INGRESS_HANDLER_PROJECT_ID="+p.ProjectID,
		"INGRESS_HANDLER_RELAY_TOKEN="+p.RelayToken,
		"INGRESS_HANDLER_WS_URL="+p.WsUrl,
		"INGRESS_HANDLER_TOKEN="+p.Token,
		"INGRESS_HANDLER_EXTRA_PARAMS="+extraParamsString,
		"INGRESS_HANDLER_FEATURE_FLAGS="+featureFlags,
		"INGRESS_HANDLER_LOGGING_FIELDS="+loggingFields,
	)

	l := utils.NewHandlerLogger(p.State.ResourceId, p.IngressId)
	cmd.Stdout = l
	cmd.Stderr = l

	return cmd, nil
}
