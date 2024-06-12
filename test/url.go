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
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/service"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
)

func RunURLTest(t *testing.T, conf *TestConfig, bus psrpc.MessageBus, commandPsrpcClient rpc.IngressHandlerClient, psrpcClient rpc.IOInfoClient, newCmd func(ctx context.Context, p *params.Params) (*exec.Cmd, error)) {
	svc, err := service.NewService(conf.Config, psrpcClient, bus, nil, nil, newCmd, "")
	require.NoError(t, err)
	svc.StartDebugHandlers()

	go func() {
		err := svc.Run()
		require.NoError(t, err)
	}()

	t.Cleanup(func() {
		svc.Stop(true)
	})

	_, err = rpc.NewIngressInternalServer(svc, bus)
	require.NoError(t, err)

	internalPsrpcClient, err := rpc.NewIngressInternalClient(bus, psrpc.WithClientTimeout(5*time.Second))
	require.NoError(t, err)

	updates := make(chan *rpc.UpdateIngressStateRequest, 10)
	ios := &ioServer{}
	ios.updateIngressState = func(req *rpc.UpdateIngressStateRequest) error {
		updates <- req
		return nil
	}

	info := &livekit.IngressInfo{
		IngressId:           "ingress_id",
		InputType:           livekit.IngressInput_URL_INPUT,
		Name:                "ingress-test",
		RoomName:            conf.RoomName,
		ParticipantIdentity: "ingress-test",
		ParticipantName:     "ingress-test",
		Reusable:            true,
		StreamKey:           "ingress-test",
		Url:                 "http://devimages.apple.com/iphone/samples/bipbop/gear4/prog_index.m3u8",
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
					},
				},
			},
		},
	}
	ios.getIngressInfo = func(req *rpc.GetIngressInfoRequest) (*rpc.GetIngressInfoResponse, error) {
		return nil, psrpc.NewErrorf(psrpc.NotFound, "not found")
	}

	ioPsrpc, err := rpc.NewIOInfoServer(ios, bus)
	require.NoError(t, err)
	t.Cleanup(func() {
		ioPsrpc.Kill()
	})

	time.Sleep(time.Second)

	logger.Infow("http pull url", "url", info.Url)

	info2, err := internalPsrpcClient.StartIngress(context.Background(), &rpc.StartIngressRequest{Info: info})
	require.NoError(t, err)
	require.Equal(t, info2.State.Status, livekit.IngressState_ENDPOINT_BUFFERING)

	time.Sleep(time.Second * 45)

	_, err = commandPsrpcClient.DeleteIngress(context.Background(), info.IngressId, &livekit.DeleteIngressRequest{IngressId: info.IngressId})
	require.NoError(t, err)

	time.Sleep(time.Second * 2)

	final := <-updates
	require.NotEqual(t, final.State.Status, livekit.IngressState_ENDPOINT_ERROR)
}
