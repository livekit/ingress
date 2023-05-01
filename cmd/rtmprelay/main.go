package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/rtmp"
	"github.com/livekit/ingress/pkg/service"
	"github.com/livekit/protocol/logger"
)

func main() {
	conf := &config.Config{
		RTMPPort:      1935,
		HTTPRelayPort: 9090,
	}

	rtmpServer := rtmp.NewRTMPServer()
	relay := service.NewRelay(rtmpServer)

	err := rtmpServer.Start(conf, nil)
	if err != nil {
		panic(fmt.Sprintf("Failed starting RTMP server %s", err))
	}
	err = relay.Start(conf)
	if err != nil {
		panic(fmt.Sprintf("Failed starting RTMP relay %s", err))
	}

	killChan := make(chan os.Signal, 1)
	signal.Notify(killChan, syscall.SIGINT)

	sig := <-killChan
	logger.Infow("exit requested, shutting down", "signal", sig)
	rtmpServer.Stop()
}
