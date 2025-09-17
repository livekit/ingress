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
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/service"
	"github.com/livekit/ingress/pkg/whip"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
)

const (
	whipClientPath = "livekit-whip-bot/cmd/whip-client/whip-client"
)

func RunWHIPTest(t *testing.T, conf *TestConfig, bus psrpc.MessageBus, commandPsrpcClient rpc.IngressHandlerClient, psrpcClient rpc.IOInfoClient, newCmd func(ctx context.Context, p *params.Params) (*exec.Cmd, error)) {
	whipsrv, err := whip.NewWHIPServer(bus)
	require.NoError(t, err)
	relay := service.NewRelay(nil, whipsrv)

	svc, err := service.NewService(conf.Config, psrpcClient, bus, nil, whipsrv, newCmd, "")
	require.NoError(t, err)
	go func() {
		err := svc.Run()
		require.NoError(t, err)
	}()

	err = whipsrv.Start(conf.Config, svc.HandleWHIPPublishRequest, nil, svc.GetHealthHandlers())
	require.NoError(t, err)
	err = relay.Start(conf.Config)
	require.NoError(t, err)

	t.Cleanup(func() {
		relay.Stop()
		whipsrv.Stop()
		svc.Stop(true)
	})

	updates := make(chan *rpc.UpdateIngressStateRequest, 10)
	ios := &ioServer{}
	ios.updateIngressState = func(req *rpc.UpdateIngressStateRequest) error {
		updates <- req
		return nil
	}

	tr := true

	info := &livekit.IngressInfo{
		IngressId:           "ingress_id",
		InputType:           livekit.IngressInput_WHIP_INPUT,
		Name:                "ingress-test",
		RoomName:            conf.RoomName,
		ParticipantIdentity: "ingress-test",
		ParticipantName:     "ingress-test",
		Reusable:            true,
		StreamKey:           "ingress-test",
		Url:                 "http://localhost:8080/w",
		EnableTranscoding:   &tr,
		Audio: &livekit.IngressAudioOptions{
			Name:   "audio",
			Source: 0,
			EncodingOptions: &livekit.IngressAudioOptions_Options{
				Options: &livekit.IngressAudioEncodingOptions{
					AudioCodec: livekit.AudioCodec_OPUS,
					Bitrate:    64000,
					DisableDtx: false,
					Channels:   2,
				},
			},
		},
		Video: &livekit.IngressVideoOptions{
			Name:   "video",
			Source: 0,
			EncodingOptions: &livekit.IngressVideoOptions_Options{
				Options: &livekit.IngressVideoEncodingOptions{
					VideoCodec: livekit.VideoCodec_H264_BASELINE,
					FrameRate:  20,
					Layers: []*livekit.VideoLayer{
						{
							Quality: livekit.VideoQuality_HIGH,
							Width:   1280,
							Height:  720,
							Bitrate: 3000000,
						},
						{
							Quality: livekit.VideoQuality_LOW,
							Width:   640,
							Height:  360,
							Bitrate: 1000000,
						},
					},
				},
			},
		},
	}
	ios.getIngressInfo = func(req *rpc.GetIngressInfoRequest) (*rpc.GetIngressInfoResponse, error) {
		return &rpc.GetIngressInfoResponse{Info: info, WsUrl: conf.WsUrl}, nil
	}

	ioPsrpc, err := rpc.NewIOInfoServer(ios, bus)
	require.NoError(t, err)
	t.Cleanup(func() {
		ioPsrpc.Kill()
	})

	time.Sleep(1 * time.Second)

	logger.Infow("whip url", "url", info.Url, "streamKey", info.StreamKey)

	whipUrl := fmt.Sprintf("%s/%s", info.Url, info.StreamKey)

	cmdString := strings.Split(
		fmt.Sprintf("%s -url %s", whipClientPath, whipUrl),
		" ")
	cmd := exec.Command(cmdString[0], cmdString[1:]...)
	require.NoError(t, cmd.Start())

	t.Cleanup(func() {
		syscall.Kill(cmd.Process.Pid, syscall.SIGTERM)
	})

	time.Sleep(time.Second * 45)

	_, err = commandPsrpcClient.DeleteIngress(context.Background(), info.IngressId, &livekit.DeleteIngressRequest{IngressId: info.IngressId})
	require.NoError(t, err)

	time.Sleep(time.Second * 2)

	final := <-updates
	require.NotEqual(t, final.State.Status, livekit.IngressState_ENDPOINT_ERROR)

}
