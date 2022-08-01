package test

import (
	"context"
	"fmt"
	"io/ioutil"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/rtmp"
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

	conf := tc.Config
	conf.RTMPPort = 1935
	conf.HTTPRelayPort = 9090

	rc, err := redis.GetRedisClient(conf.Redis)
	require.NoError(t, err)
	require.NotNil(t, rc, "redis required")

	rpcServer := ingress.NewRedisRPC("ingress_test_service", rc)
	rpcClient := ingress.NewRedisRPC("ingress_test_client", rc)

	svc := service.NewService(conf, rpcServer)

	rtmpsrv := rtmp.NewRTMPServer()
	relay := rtmp.NewRTMPRelay(rtmpsrv)

	err = rtmpsrv.Start(conf, svc.HandleRTMPPublishRequest)
	require.NoError(t, err)
	err = relay.Start(conf)
	require.NoError(t, err)

	t.Cleanup(func() {
		relay.Stop()
		rtmpsrv.Stop()
	})

	go func() {
		err := svc.Run()
		require.NoError(t, err)
	}()
	time.Sleep(time.Second)
	t.Cleanup(func() { svc.Stop(true) })

	ctx := context.Background()
	updates, err := rpcClient.GetUpdateChannel(ctx)
	require.NoError(t, err)

	info := &livekit.IngressInfo{
		InputType:           livekit.IngressInput_RTMP_INPUT,
		Name:                "ingress-test",
		RoomName:            tc.RoomName,
		ParticipantIdentity: "ingress-test",
		ParticipantName:     "ingress-test",
		Reusable:            true,
		StreamKey:           "ingress-test",
		Url:                 "rtmp://localhost:1935/live/ingress-test",
		Audio: &livekit.IngressAudioOptions{
			Name:       "audio",
			Source:     0,
			MimeType:   webrtc.MimeTypeOpus,
			Bitrate:    64000,
			DisableDtx: false,
			Channels:   2,
		},
		Video: &livekit.IngressVideoOptions{
			Name:     "video",
			Source:   0,
			MimeType: webrtc.MimeTypeH264,
			Layers: []*livekit.VideoLayer{
				{
					Quality: livekit.VideoQuality_HIGH,
					Width:   1280,
					Height:  720,
					Bitrate: 3000,
				},
			},
		},
	}

	shutdown := make(chan struct{})
	go func() {
		pubSub, err := rpcClient.GetEntityChannel(context.Background())
		require.NoError(t, err)

		for {
			select {
			case msg := <-pubSub.Channel():
				b := pubSub.Payload(msg)

				req := &livekit.GetIngressInfoRequest{}
				if err = proto.Unmarshal(b, req); err != nil {
					logger.Errorw("failed to read request", err)
					continue
				}

				err = rpcClient.SendResponse(context.Background(), req, info, err)
				if err != nil {
					logger.Errorw("could not send response", err)
				}

			case <-shutdown:
				_ = pubSub.Close()
				return
			}
		}
	}()

	require.NoError(t, err)
	require.NotEmpty(t, info.Url)

	logger.Infow("rtmp url", "url", info.Url)

	cmdString := strings.Split(
		fmt.Sprintf(
			"gst-launch-1.0 -v flvmux name=mux ! rtmp2sink location=%s  "+
				"audiotestsrc freq=200 ! faac ! mux.  "+
				"videotestsrc pattern=ball is-live=true ! video/x-raw,width=1280,height=720 ! x264enc speed-preset=3 tune=zerolatency ! mux.",
			info.Url),
		" ")
	cmd := exec.Command(cmdString[0], cmdString[1:]...)
	require.NoError(t, cmd.Start())

	time.Sleep(time.Second * 45)

	info, err = rpcClient.SendRequest(ctx, &livekit.IngressRequest{
		IngressId: info.IngressId,
		Request:   &livekit.IngressRequest_Delete{Delete: &livekit.DeleteIngressRequest{IngressId: info.IngressId}},
	})
	require.NoError(t, err)

	close(shutdown)

	msg := <-updates.Channel()
	b := updates.Payload(msg)

	final := &livekit.IngressInfo{}
	require.NoError(t, proto.Unmarshal(b, final))
	require.NotEqual(t, final.State.Status, livekit.IngressState_ENDPOINT_ERROR)
}
