package main

import (
	"fmt"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/rtmp"
)

func main() {
	fmt.Println("1")
	conf := &config.Config{}

	rtmpServer := rtmp.NewRTMPServer()
	fmt.Println("2")
	relay := rtmp.NewRTMPRelay(rtmpServer)

	fmt.Println("3")
	err := rtmpServer.Start(conf)
	if err != nil {
		panic(fmt.Sprintf("Failed starting RTMP server %s", err))
	}
	fmt.Println("4")
	err = relay.Start(conf)
	if err != nil {
		panic(fmt.Sprintf("Failed starting RTMP relay %s", err))
	}
}
