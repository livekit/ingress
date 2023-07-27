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
