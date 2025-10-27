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
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/tracer"
	"github.com/livekit/psrpc"
	google_protobuf2 "google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/ingress/pkg/utils"
)

const (
	sessionNotifyTimeout = 3 * time.Minute
)

type proxyWhipHandler struct {
	logger        logger.Logger
	params        *params.Params
	location      *url.URL
	ua            string
	participantID string

	done      core.Fuse
	rpcServer rpc.IngressHandlerServer

	mu       sync.Mutex
	watchdog *time.Timer
}

func NewProxyWHIPHandler(p *params.Params, bus psrpc.MessageBus, ua string) (WHIPHandler, error) {
	// RPC is handled in the handler process when transcoding

	h := &proxyWhipHandler{
		params: p,
		logger: p.GetLogger(),
		ua:     ua,
	}

	rpcServer, err := rpc.NewIngressHandlerServer(h, bus)
	if err != nil {
		return nil, err
	}

	err = utils.RegisterIngressRpcHandlers(rpcServer, p.IngressInfo)
	if err != nil {
		return nil, err
	}

	h.rpcServer = rpcServer

	return h, nil
}

func getErrorCodeForStatus(statusCode int) psrpc.ErrorCode {
	switch statusCode {
	case 200, 201:
		return psrpc.OK
	case 400:
		return psrpc.InvalidArgument
	case 401:
		return psrpc.Unauthenticated
	case 403:
		return psrpc.PermissionDenied
	case 404:
		return psrpc.NotFound
	default:
		return psrpc.Internal
	}
}

func (h *proxyWhipHandler) Init(ctx context.Context, sdpOffer string) (string, error) {
	protocol := "http"
	urlBase := ""
	switch {
	case strings.HasPrefix(h.params.WsUrl, "wss://"):
		protocol = "https"
		urlBase = strings.TrimPrefix(h.params.WsUrl, "wss://")
	case strings.HasPrefix(h.params.WsUrl, "https://"):
		protocol = "https"
		urlBase = strings.TrimPrefix(h.params.WsUrl, "https://")
	case strings.HasPrefix(h.params.WsUrl, "ws://"):
		urlBase = strings.TrimPrefix(h.params.WsUrl, "ws://")
	case strings.HasPrefix(h.params.WsUrl, "http://"):
		urlBase = strings.TrimPrefix(h.params.WsUrl, "http://")

	default:
		return "", psrpc.NewErrorf(psrpc.InvalidArgument, "Invalid wsURL")
	}

	urlObj, err := url.Parse(fmt.Sprintf("%s://%s/whip/v1", protocol, urlBase))
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", urlObj.String(), bytes.NewReader([]byte(sdpOffer)))
	if err != nil {
		return "", err
	}

	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", h.params.Token))
	req.Header.Add("Content-Type", "application/sdp")
	req.Header.Add("X-Livekit-Ingress", "true")
	if h.ua != "" {
		req.Header.Set("User-Agent", h.ua)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	h.logger.Infow("requested WHIP resource creation", "url", urlObj, "statusCode", resp.StatusCode)

	code := getErrorCodeForStatus(resp.StatusCode)
	if code != psrpc.OK {
		return "", psrpc.NewErrorf(code, "WHIP resource creation failed on SFU. code=%d", resp.StatusCode)
	}

	locationHeader := resp.Header.Get("Location")
	if locationHeader == "" {
		return "", psrpc.NewErrorf(psrpc.MalformedResponse, "Missing Location header in SFU WHIP response")
	}

	locationHeaderUrl, err := url.Parse(locationHeader)
	if err != nil {
		return "", err
	}

	location := urlObj.ResolveReference(locationHeaderUrl)

	h.location = location
	h.participantID = location.Path[strings.LastIndex(location.Path, "/")+1:]
	if h.participantID != "" {
		if err = h.rpcServer.RegisterWHIPRTCConnectionNotifyTopic(h.participantID); err != nil {
			return "", err
		}
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(b), nil
}

func (h *proxyWhipHandler) SetMediaStatsGatherer(st *stats.LocalMediaStatsGatherer) {
}

func (h *proxyWhipHandler) Start(ctx context.Context) (map[types.StreamKind]string, error) {
	return nil, nil
}

func (h *proxyWhipHandler) Close() {
	h.close(false)
}

func (h *proxyWhipHandler) close(isRTCClosed bool) {
	if !h.done.Break() {
		return
	}

	h.mu.Lock()
	if h.watchdog != nil {
		h.watchdog.Stop()
		h.watchdog = nil
	}
	h.mu.Unlock()

	utils.DeregisterIngressRpcHandlers(h.rpcServer, h.params.IngressInfo)
	if h.participantID != "" {
		if isRTCClosed {
			h.rpcServer.DeregisterWHIPRTCConnectionNotifyTopic(h.participantID)
		} else {
			// wait a bit before deregistering, in case sfu would send rtc closed notify
			time.AfterFunc(5*time.Second, func() { h.rpcServer.DeregisterWHIPRTCConnectionNotifyTopic(h.participantID) })
		}
	}

	h.logger.Infow("closing WHIP session", "location", h.location, "isRTCClosed", isRTCClosed)

	if h.location == nil || isRTCClosed {
		return
	}

	req, err := http.NewRequest("DELETE", h.location.String(), nil)
	if err != nil {
		h.logger.Warnw("WHIP DELETE request creation failed", err)
		return
	}

	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", h.params.Token))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.logger.Warnw("WHIP DELETE request failed", err)
		return
	}
	defer resp.Body.Close()

	h.logger.Infow("requested WHIP resource deletion", "url", h.location, "statusCode", resp.StatusCode)

	code := getErrorCodeForStatus(resp.StatusCode)
	if code != psrpc.OK {
		err = psrpc.NewErrorf(code, "WHIP resource deletion failed on SFU. code=%d", resp.StatusCode)
		h.logger.Warnw("WHIP delete request returned error status code", err, "statusCode", resp.StatusCode)
		return
	}
}

func (h *proxyWhipHandler) WaitForSessionEnd(ctx context.Context) error {
	h.resetWatchDog()

	select {
	case <-h.done.Watch():
	case <-ctx.Done():
	}

	return nil
}

func (h *proxyWhipHandler) AssociateRelay(kind types.StreamKind, token string, w io.WriteCloser) error {
	return nil
}

func (h *proxyWhipHandler) DissociateRelay(kind types.StreamKind) {
}

func (h *proxyWhipHandler) UpdateIngress(ctx context.Context, req *livekit.UpdateIngressRequest) (*livekit.IngressState, error) {
	_, span := tracer.Start(ctx, "proxyWhipHandler.UpdateIngress")
	defer span.End()

	h.Close()

	return h.params.CopyInfo().State, nil
}

func (h *proxyWhipHandler) DeleteIngress(ctx context.Context, req *livekit.DeleteIngressRequest) (*livekit.IngressState, error) {
	_, span := tracer.Start(ctx, "proxyWhipHandler.DeleteIngress")
	defer span.End()

	h.Close()

	return h.params.CopyInfo().State, nil
}

func (h *proxyWhipHandler) DeleteWHIPResource(ctx context.Context, req *rpc.DeleteWHIPResourceRequest) (*google_protobuf2.Empty, error) {
	_, span := tracer.Start(ctx, "proxyWhipHandler.DeleteWHIPResource")
	defer span.End()

	if h.params.StreamKey != req.StreamKey {
		h.logger.Infow("received delete request with wrong stream key", "streamKey", req.StreamKey)
	}

	h.Close()

	return &google_protobuf2.Empty{}, nil
}

func (h *proxyWhipHandler) ICERestartWHIPResource(ctx context.Context, req *rpc.ICERestartWHIPResourceRequest) (*rpc.ICERestartWHIPResourceResponse, error) {

	h.logger.Infow("closing WHIP session", "location", h.location)

	if h.location == nil {
		return nil, psrpc.NewErrorf(psrpc.FailedPrecondition, "WHIP session not established")
	}

	wreq, err := http.NewRequest("PATCH", h.location.String(), bytes.NewReader([]byte(req.RawTrickleIceSdpfrag)))
	if err != nil {
		return nil, err
	}

	wreq.Header.Add("Content-type", "application/trickle-ice-sdpfrag")
	wreq.Header.Add("Authorization", fmt.Sprintf("Bearer %s", h.params.Token))
	wreq.Header.Add("If-Match", req.IfMatch)

	resp, err := http.DefaultClient.Do(wreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	h.logger.Infow("requested WHIP resource deletion", "url", h.location, "statusCode", resp.StatusCode)

	code := getErrorCodeForStatus(resp.StatusCode)
	if code != psrpc.OK {
		return nil, psrpc.NewErrorf(code, "WHIP resource patch failed on SFU. code=%d", resp.StatusCode)
	}

	sdpResponse, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return &rpc.ICERestartWHIPResourceResponse{
		TrickleIceSdpfrag: string(sdpResponse),
		Etag:              resp.Header.Get("ETag"),
	}, nil
}

func (h *proxyWhipHandler) WHIPRTCConnectionNotify(ctx context.Context, req *rpc.WHIPRTCConnectionNotifyRequest) (*google_protobuf2.Empty, error) {
	tctx, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()

	h.resetWatchDog()

	if req.Video != nil {
		h.params.SetInputVideoState(tctx, req.Video, true)
	}

	if req.Audio != nil {
		h.params.SetInputAudioState(tctx, req.Audio, true)
	}

	if req.Closed {
		h.close(true)
	}

	return &google_protobuf2.Empty{}, nil
}

func (h *proxyWhipHandler) resetWatchDog() {
	h.mu.Lock()
	if h.watchdog != nil {
		h.watchdog.Stop()
	}

	h.watchdog = time.AfterFunc(sessionNotifyTimeout, func() {
		h.logger.Infow("no Notify call from the SFU, terminating ingress")
		h.done.Break()
	})
	h.mu.Unlock()
}
