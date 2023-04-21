//go:build integration

package test

import (
	"context"
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	google_protobuf2 "google.golang.org/protobuf/types/known/emptypb"
	"gopkg.in/yaml.v3"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
)

type TestConfig struct {
	*config.Config `yaml:",inline"`
	RoomName       string `yaml:"room_name"`
	RtmpOnly       bool   `yaml:"rtmp_only"`
	WhipOnly       bool   `yaml:"whip_only"`
}

type ioServer struct {
	getIngressInfo     func(*rpc.GetIngressInfoRequest) (*rpc.GetIngressInfoResponse, error)
	updateIngressState func(*rpc.UpdateIngressStateRequest) error
}

func (s *ioServer) UpdateEgressInfo(context.Context, *livekit.EgressInfo) (*google_protobuf2.Empty, error) {
	return &google_protobuf2.Empty{}, nil
}

func (s *ioServer) GetIngressInfo(ctx context.Context, req *rpc.GetIngressInfoRequest) (*rpc.GetIngressInfoResponse, error) {
	return s.getIngressInfo(req)
}

func (s *ioServer) UpdateIngressState(ctx context.Context, req *rpc.UpdateIngressStateRequest) (*google_protobuf2.Empty, error) {
	return &google_protobuf2.Empty{}, s.updateIngressState(req)
}

func GetDefaultConfig(t *testing.T) *TestConfig {
	tc := &TestConfig{Config: &config.Config{}}
	// Defaults
	tc.RTMPPort = 1935
	tc.HTTPRelayPort = 9090
	tc.WHIPPort = 8080

	tc.NodeID = "INGRESS_TEST"

	return tc
}

func getConfig(t *testing.T) *TestConfig {
	tc := GetDefaultConfig(t)

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

func RunTestSuite(t *testing.T, conf *TestConfig, bus psrpc.MessageBus) {
	if !conf.WhipOnly {
		RunRTMPTest(t, conf, bus)
	}
	if !conf.RtmpOnly {
		RunWHIPTest(t, conf, bus)
	}
}
