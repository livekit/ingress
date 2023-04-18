package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/service"
	"github.com/livekit/ingress/pkg/whip"
	"github.com/livekit/protocol/logger"
)

func main() {
	conf := &config.Config{
		WHIPPort:      8080,
		HTTPRelayPort: 9090,
	}

	whipServer := whip.NewWHIPServer()
	relay := service.NewRelay(nil, whipServer)

	err := whipServer.Start(conf, func(streamKey, resourceId, sdpOffer string) error {
		logger.Infow("new whip client", "streamKey", streamKey, "resourceId", resourceId)

		sdpPayload := "THIS IS A TEST SDP ANSWER"

		_, err := http.Post(fmt.Sprintf("http://localhost:%d/whip/%s", conf.HTTPRelayPort, resourceId), "application/sdp", bytes.NewReader([]byte(sdpPayload)))
		if err != nil {
			logger.Errorw("relay POST failed", err)
		}

		return nil
	})
	if err != nil {
		panic(fmt.Sprintf("Failed starting WHIP server %s", err))
	}
	err = relay.Start(conf)
	if err != nil {
		panic(fmt.Sprintf("Failed starting WHIP relay %s", err))
	}

	sdpPayload := "THIS IS A TEST SDP OFFER"

	resp, err := http.Post(fmt.Sprintf("http://localhost:%d/w/%s", conf.WHIPPort, "stream_key"), "application/sdp", bytes.NewReader([]byte(sdpPayload)))
	if err != nil {
		panic(fmt.Sprintf("relay POST failed %s", err))
	}
	defer resp.Body.Close()

	sdpAnswer := bytes.Buffer{}
	_, err = io.Copy(&sdpAnswer, resp.Body)
	if err != nil {
		panic(fmt.Sprintf("can't read response %s", err))
	}

	fmt.Printf("Response status %d %s\n", resp.StatusCode, resp.Status)
	fmt.Printf("location %s\n", resp.Header.Get("Location"))
	fmt.Println(string(sdpAnswer.Bytes()))
}
