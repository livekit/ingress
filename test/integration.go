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

//go:build integration

package test

import (
	"context"
	"io/ioutil"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
	"gopkg.in/yaml.v3"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/params"
)

type TestConfig struct {
	*config.Config `yaml:",inline"`
	RoomName       string `yaml:"room_name"`
	RtmpOnly       bool   `yaml:"rtmp_only"`
	WhipOnly       bool   `yaml:"whip_only"`
	URLOnly        bool   `yaml:"url_only"`
}

type ioServer struct {
	getIngressInfo     func(*rpc.GetIngressInfoRequest) (*rpc.GetIngressInfoResponse, error)
	updateIngressState func(*rpc.UpdateIngressStateRequest) error
}

func (s *ioServer) CreateEgress(_ context.Context, _ *livekit.EgressInfo) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (s *ioServer) GetEgress(_ context.Context, _ *rpc.GetEgressRequest) (*livekit.EgressInfo, error) {
	return nil, nil
}

func (s *ioServer) ListEgress(_ context.Context, _ *livekit.ListEgressRequest) (*livekit.ListEgressResponse, error) {
	return nil, nil
}

func (s *ioServer) UpdateEgress(_ context.Context, _ *livekit.EgressInfo) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (s *ioServer) UpdateMetrics(_ context.Context, _ *rpc.UpdateMetricsRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (s *ioServer) GetIngressInfo(_ context.Context, req *rpc.GetIngressInfoRequest) (*rpc.GetIngressInfoResponse, error) {
	return s.getIngressInfo(req)
}

func (s *ioServer) CreateIngress(_ context.Context, _ *livekit.IngressInfo) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (s *ioServer) UpdateIngressState(_ context.Context, req *rpc.UpdateIngressStateRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, s.updateIngressState(req)
}

func (s *ioServer) EvaluateSIPDispatchRules(_ context.Context, _ *rpc.EvaluateSIPDispatchRulesRequest) (*rpc.EvaluateSIPDispatchRulesResponse, error) {
	return nil, nil
}

func (s *ioServer) GetSIPTrunkAuthentication(_ context.Context, _ *rpc.GetSIPTrunkAuthenticationRequest) (*rpc.GetSIPTrunkAuthenticationResponse, error) {
	return nil, nil
}

func (s *ioServer) UpdateSIPCallState(_ context.Context, _ *rpc.UpdateSIPCallStateRequest) (*emptypb.Empty, error) {
	return nil, nil
}

func (s *ioServer) RecordCallContext(context.Context, *rpc.RecordCallContextRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func GetDefaultConfig() *TestConfig {
	tc := &TestConfig{
		Config: &config.Config{
			ServiceConfig:  &config.ServiceConfig{},
			InternalConfig: &config.InternalConfig{},
		},
	}
	// Defaults
	tc.RTMPPort = 1935
	tc.HTTPRelayPort = 9090
	tc.WHIPPort = 8080

	tc.NodeID = "INGRESS_TEST"

	return tc
}

func getConfig(t *testing.T) *TestConfig {
	tc := GetDefaultConfig()

	confString := os.Getenv("INGRESS_CONFIG_BODY")
	if confString == "" {
		confFile := os.Getenv("INGRESS_CONFIG_FILE")
		require.NotEmpty(t, confFile)
		b, err := ioutil.ReadFile(confFile)
		require.NoError(t, err)
		confString = string(b)
	}

	require.NoError(t, yaml.Unmarshal([]byte(confString), tc))
	tc.InitLogger()

	return tc
}

func RunTestSuite(t *testing.T, conf *TestConfig, bus psrpc.MessageBus, newCmd func(ctx context.Context, p *params.Params) (*exec.Cmd, error)) {
	psrpcClient, err := rpc.NewIOInfoClient(bus)
	require.NoError(t, err)

	conf.Config.RTCConfig.Validate(conf.Development)
	conf.Config.RTCConfig.EnableLoopbackCandidate = true

	commandPsrpcClient, err := rpc.NewIngressHandlerClient(bus, psrpc.WithClientTimeout(5*time.Second))
	require.NoError(t, err)

	if !conf.WhipOnly && !conf.URLOnly {
		t.Run("RTMP", func(t *testing.T) {
			RunRTMPTest(t, conf, bus, commandPsrpcClient, psrpcClient, newCmd)
		})
	}
	if !conf.RtmpOnly && !conf.URLOnly {
		t.Run("WHIP", func(t *testing.T) {
			RunWHIPTest(t, conf, bus, commandPsrpcClient, psrpcClient, newCmd)
		})
	}
	if !conf.RtmpOnly && !conf.WhipOnly {
		t.Run("URL pul", func(t *testing.T) {
			RunURLTest(t, conf, bus, commandPsrpcClient, psrpcClient, newCmd)
		})
	}

}
