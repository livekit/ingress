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

package service

import (
	"fmt"
	"net/http"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/rtmp"
	"github.com/livekit/ingress/pkg/whip"
	"github.com/livekit/protocol/logger"
)

type Relay struct {
	server     *http.Server
	rtmpServer *rtmp.RTMPServer
	whipServer *whip.WHIPServer
}

func NewRelay(rtmpServer *rtmp.RTMPServer, whipServer *whip.WHIPServer) *Relay {
	return &Relay{
		rtmpServer: rtmpServer,
		whipServer: whipServer,
	}
}

func (r *Relay) Start(conf *config.Config) error {
	port := conf.HTTPRelayPort

	mux := http.NewServeMux()

	if r.rtmpServer != nil {
		h := rtmp.NewRTMPRelayHandler(r.rtmpServer)
		mux.Handle("/rtmp/", h)
	}
	if r.whipServer != nil {
		h := whip.NewWHIPRelayHandler(r.whipServer)
		mux.Handle("/whip/", h)
	}

	r.server = &http.Server{
		Handler: mux,
		Addr:    fmt.Sprintf("localhost:%d", port),
	}

	go func() {
		err := r.server.ListenAndServe()
		logger.Debugw("Relay stopped", "error", err)
	}()

	return nil
}

func (r *Relay) Stop() error {
	return r.server.Close()
}
