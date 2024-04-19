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

package whip

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/psrpc"
)

type WHIPRelayHandler struct {
	whipServer *WHIPServer
}

func NewWHIPRelayHandler(whipServer *WHIPServer) *WHIPRelayHandler {
	return &WHIPRelayHandler{
		whipServer: whipServer,
	}
}

func (h *WHIPRelayHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var err error
	defer func() {
		var psrpcErr psrpc.Error

		switch {
		case errors.As(err, &psrpcErr):
			w.WriteHeader(psrpcErr.ToHttp())
		case err == nil:
			// Nothing, we already responded
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}()

	path := strings.TrimLeft(r.URL.Path, "/whip/") //nolint
	v := strings.Split(path, "/")
	if len(v) != 2 {
		err = psrpc.NewErrorf(psrpc.NotFound, "invalid path")
		return
	}
	resourceId := v[0]
	kind := types.StreamKind(v[1])
	token := r.URL.Query().Get("token")

	log := logger.Logger(logger.GetLogger().WithValues("resourceId", resourceId, "kind", kind))
	log.Infow("relaying whip ingress")

	pr, pw := io.Pipe()
	done := make(chan error)

	go func() {
		b := make([]byte, 2000)
		var err error
		var n int
		for {
			n, err = pr.Read(b)
			if err != nil {
				break
			}

			_, err = w.Write(b[:n])
			if err != nil {
				break
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}

		if err == io.EOF {
			err = nil
		}

		done <- err
		close(done)
	}()

	defer func() {
		pw.Close()
		h.whipServer.DissociateRelay(resourceId, kind)
	}()

	err = h.whipServer.AssociateRelay(resourceId, kind, token, pw)
	if err != nil {
		return
	}

	err = <-done
}
