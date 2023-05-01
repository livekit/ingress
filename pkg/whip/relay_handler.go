package whip

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"

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

	resourceId := strings.TrimLeft(r.URL.Path, "/whip/")

	sdp := bytes.Buffer{}

	_, err = io.Copy(&sdp, r.Body)
	if err != nil {
		h.whipServer.SetSDPResponse(resourceId, "", err)
	}

	h.whipServer.SetSDPResponse(resourceId, string(sdp.Bytes()), nil)
}
