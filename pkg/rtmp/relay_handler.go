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

package rtmp

import (
	"io"
	"net/http"
	"strings"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/psrpc"
)

type RTMPRelayHandler struct {
	rtmpServer *RTMPServer
}

func NewRTMPRelayHandler(rtmpServer *RTMPServer) *RTMPRelayHandler {
	return &RTMPRelayHandler{
		rtmpServer: rtmpServer,
	}
}

func (h *RTMPRelayHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	resourceId := strings.TrimLeft(r.URL.Path, "/rtmp/")
	token := r.URL.Query().Get("token")

	log := logger.Logger(logger.GetLogger().WithValues("resourceID", resourceId))
	log.Infow("relaying ingress")

	pr, pw := io.Pipe()
	done := make(chan error, 1)

	go func() {
		_, err := io.Copy(w, pr)
		done <- err
	}()

	if err := h.rtmpServer.AssociateRelay(resourceId, token, pw); err != nil {
		// Ensure the copy goroutine exits before we respond with an error.
		_ = pw.CloseWithError(err)
		<-done

		var psrpcErr psrpc.Error
		status := http.StatusInternalServerError
		if errors.As(err, &psrpcErr) {
			status = psrpcErr.ToHttp()
		}

		log.Warnw("failed to associate RTMP relay", err, "status", status)
		w.WriteHeader(status)

		return
	}
	defer func() {
		pw.Close()
		h.rtmpServer.DissociateRelay(resourceId)
	}()

	if err := <-done; err != nil && !errors.Is(err, io.ErrClosedPipe) {
		log.Warnw("relay stream ended with error", err)
	}
}
