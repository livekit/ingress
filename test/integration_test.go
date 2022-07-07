package test

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/service"
	"github.com/livekit/protocol/ingress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/redis"
)

type TestConfig struct {
	*config.Config `yaml:",inline"`
	RoomName       string `yaml:"room_name"`
}

func TestIngress(t *testing.T) {
	confString, err := ioutil.ReadFile("config.yaml")
	require.NoError(t, err)

	tc := &TestConfig{}
	require.NoError(t, yaml.Unmarshal(confString, tc))
	tc.NodeID = "INGRESS_TEST"
	tc.InitLogger()

	rtsp := exec.Command("./rtsp-simple-server")
	rtsp.Stdout = os.Stdout
	rtsp.Stderr = os.Stderr
	rtsp.Dir = "../bin"
	require.NoError(t, rtsp.Start())
	t.Cleanup(func() { _ = rtsp.Process.Kill() })

	conf := tc.Config

	rc, err := redis.GetRedisClient(conf.Redis)
	require.NoError(t, err)
	require.NotNil(t, rc, "redis required")

	rpcServer := ingress.NewRedisRPCServer(rc)
	rpcClient := ingress.NewRedisRPCClient("ingress_test", rc)

	svc := service.NewService(conf, rpcServer)
	go func() {
		err := svc.Run()
		require.NoError(t, err)
	}()
	time.Sleep(time.Second)
	t.Cleanup(func() { svc.Stop(true) })

	ctx := context.Background()
	info, err := rpcClient.SendRequest(ctx, &livekit.StartIngressRequest{
		Request: &livekit.CreateIngressRequest{
			InputType:           livekit.IngressInput_RTMP_INPUT,
			Name:                "ingress-test",
			RoomName:            tc.RoomName,
			ParticipantIdentity: "ingress-test",
			ParticipantName:     "ingress-test",
			Audio: &livekit.IngressAudioOptions{
				Name:     "audio",
				Source:   0,
				MimeType: "audio/opus",
				Bitrate:  48000,
				Dtx:      false,
				Channels: 2,
			},
			Video: &livekit.IngressVideoOptions{
				Name:     "video",
				Source:   0,
				MimeType: "video/h264",
				Layers: []*livekit.VideoLayer{
					{
						Quality: livekit.VideoQuality_HIGH,
						Width:   1920,
						Height:  1080,
						Bitrate: 3000,
					},
				},
			},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, info.Url)

	logger.Infow("rtmp url", "url", info.Url)

	cmdString := strings.Split(
		fmt.Sprintf(
			"gst-launch-1.0 -v videotestsrc pattern=ball ! video/x-raw,width=1280,height=720 ! x264enc ! flvmux ! rtmp2sink location=%s",
			info.Url),
		" ")
	cmd := exec.Command(cmdString[0], cmdString[1:]...)
	require.NoError(t, cmd.Start())

	time.Sleep(time.Second * 15)
	t.FailNow()
}
