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
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/pprof"
	"github.com/livekit/psrpc"
)

const (
	pprofApp = "pprof"
)

func (s *Service) StartDebugHandlers() {
	if s.conf.DebugHandlerPort == 0 {
		logger.Debugw("debug handler disabled")
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/%s/", pprofApp), s.handlePProf)

	go func() {
		addr := fmt.Sprintf(":%d", s.conf.DebugHandlerPort)
		logger.Debugw(fmt.Sprintf("starting debug handler on address %s", addr))
		_ = http.ListenAndServe(addr, mux)
	}()
}

// URL path format is "/<application>/<resource_id>/<profile_name>" or "/<application>/<profile_name>" to profile the service
func (s *Service) handlePProf(w http.ResponseWriter, r *http.Request) {
	var err error
	var b []byte

	timeout, _ := strconv.Atoi(r.URL.Query().Get("timeout"))
	debug, _ := strconv.Atoi(r.URL.Query().Get("debug"))

	pathElements := strings.Split(r.URL.Path, "/")
	switch len(pathElements) {
	case 3:
		// profile main service
		b, err = pprof.GetProfileData(context.Background(), pathElements[2], timeout, debug)

	case 4:
		resourceID := pathElements[2]
		api, err := s.sm.GetIngressSessionAPI(resourceID)
		if err != nil {
			http.Error(w, "resource not found", http.StatusNotFound)
			return
		}

		b, err = api.GetProfileData(context.Background(), pathElements[3], timeout, debug)
		if err != nil {
			return
		}
	default:
		http.Error(w, "malformed url", http.StatusNotFound)
		return
	}

	if err == nil {
		w.Header().Add("Content-Type", "application/octet-stream")
		_, err = w.Write(b)
	}
	if err != nil {
		http.Error(w, err.Error(), getErrorCode(err))
		return
	}
}

func getErrorCode(err error) int {
	var e psrpc.Error

	switch {
	case errors.As(err, &e):
		return e.ToHttp()
	case err == nil:
		return http.StatusOK
	default:
		return http.StatusInternalServerError
	}
}
