//go:build integration

package test

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/livekit/ingress/pkg/service"
	"github.com/livekit/ingress/pkg/whip"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
	"github.com/stretchr/testify/require"
)

// TODO bundle tool
const (
	whipClientPath = "whip-client"
)

func RunWHIPTest(t *testing.T, conf *TestConfig, bus psrpc.MessageBus) {
	psrpcClient, err := rpc.NewIOInfoClient("ingress_test_service", bus)
	require.NoError(t, err)

	conf.Whip.EnableLoopbackCandidate = true

	svc := service.NewService(conf.Config, psrpcClient)

	commandPsrpcClient, err := rpc.NewIngressHandlerClient("ingress_test_client", bus, psrpc.WithClientTimeout(5*time.Second))
	require.NoError(t, err)

	whipsrv := whip.NewWHIPServer()
	relay := service.NewRelay(nil, whipsrv)

	err = whipsrv.Start(conf.Config, svc.HandleWHIPPublishRequest)
	require.NoError(t, err)
	err = relay.Start(conf.Config)
	require.NoError(t, err)

	t.Cleanup(func() {
		relay.Stop()
	})

	updates := make(chan *rpc.UpdateIngressStateRequest, 10)
	ios := &ioServer{}
	ios.updateIngressState = func(req *rpc.UpdateIngressStateRequest) error {
		updates <- req
		return nil
	}

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
		return &rpc.GetIngressInfoResponse{Info: info, WsUrl: conf.WsUrl}, nil
	}

	_, err = rpc.NewIOInfoServer("ingress_test_server", ios, bus)
	require.NoError(t, err)

	go func() {
		err := svc.Run()
		require.NoError(t, err)
	}()
	time.Sleep(time.Second)
	t.Cleanup(func() { svc.Stop(true) })

	logger.Infow("whip url", "url", info.Url, "streamKey", info.StreamKey)

	whipUrl := fmt.Sprintf("%s/%s", info.Url, info.StreamKey)

	cmdString := strings.Split(
		fmt.Sprintf("%s -url %s", whipClientPath, whipUrl),
		" ")
	cmd := exec.Command(cmdString[0], cmdString[1:]...)
	require.NoError(t, cmd.Start())

	time.Sleep(time.Second * 45)

	_, err = commandPsrpcClient.DeleteIngress(context.Background(), info.IngressId, &livekit.DeleteIngressRequest{IngressId: info.IngressId})
	require.NoError(t, err)

	time.Sleep(time.Second * 2)

	final := <-updates
	require.NotEqual(t, final.State.Status, livekit.IngressState_ENDPOINT_ERROR)
}
