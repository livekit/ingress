package main

import (
	"fmt"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/rtmp"
)

func main() {
	conf := &config.Config{}

	rtmpServer := rtmp.NewRTMPServer()
	relay := rtmp.NewRTMPRelay(rtmpServer)

	err := rtmpServer.Start(conf)
	if err != nil {
		panic(fmt.Sprintf("Failed starting RTMP server %s", err))
	}
	err = relay.Start(conf)
	if err != nil {
		panic(fmt.Sprintf("Failed starting RTMP relay %s", err))
	}
}
