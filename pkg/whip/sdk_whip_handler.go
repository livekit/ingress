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
	"context"
	"io"
	"time"

	"github.com/frostbyte73/core"
	"github.com/pion/webrtc/v3"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/psrpc"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

const (
	maxSignalRetryCount = 10
	maxWaitTime         = 10 * time.Second
)

type sdkWhipHandler struct {
	logger logger.Logger
	params *params.Params

	client  *lksdk.SignalClient
	answers chan webrtc.SessionDescription

	closed core.Fuse
}

func NewSDKWHIPHandler() *sdkWhipHandler {
	// Copy the rtc conf to allow modifying to to match the request

	return &sdkWhipHandler{
		client:  lksdk.NewSignalClient(),
		answers: make(chan webrtc.SessionDescription, 1),
	}
}

func (h *sdkWhipHandler) Init(ctx context.Context, p *params.Params, sdpOffer string) (string, error) {
	ctx, done := context.WithTimeout(ctx, 10*time.Second)
	defer done()

	h.logger = p.GetLogger()
	h.params = p

	h.client.OnClose = func() {
		h.logger.Infow("close event, attempting signal client reconnection")
		go func() {
			err := h.reconnect()
			if err != nil {
				h.Close()
			}
		}()
	}
	h.client.OnLeave = func(leave *livekit.LeaveRequest) {
		h.logger.Infow("leave event", "leaveRequest", leave)
		h.Close()
	}
	h.client.OnAnswer = func(sd webrtc.SessionDescription) {
		h.logger.Infow("SDP answer received", "sdp", sd)

		select {
		case h.answers <- sd:
		default:
			h.logger.Infow("dropping sdp answer")
		}
	}

	h.client.Start()

	_, err := h.client.Join(p.WsUrl, p.Token)
	if err != nil {
		return "", err
	}
	h.logger.Infow("connected to controller")

	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  sdpOffer,
	}

	err = h.client.SendOffer(offer)
	if err != nil {
		return "", err
	}

	var answer webrtc.SessionDescription
	var ok bool
	select {
	case answer, ok = <-h.answers:
		if !ok {
			return "", errors.ErrServerShuttingDown
		}
	case <-h.closed.Watch():
		return "", errors.ErrServerShuttingDown
	case <-ctx.Done():
		return "", psrpc.NewErrorf(psrpc.DeadlineExceeded, "timed out while waiting for ICE candidate gathering")
	}

	parsedAnswer, err := answer.Unmarshal()
	if err != nil {
		return "", err
	}
	for _, m := range parsedAnswer.MediaDescriptions {
		// Pion puts a media description with fmt = 0 and no attributes for unsupported codecs
		if len(m.Attributes) == 0 {
			h.logger.Infow("unsupported codec in SDP offer")
			return "", errors.ErrUnsupportedDecodeFormat
		}
	}

	return answer.SDP, nil
}

func (h *sdkWhipHandler) Start(ctx context.Context) (map[types.StreamKind]string, error) {
	// Nothing to start locally

	return nil, nil
}

func (h *sdkWhipHandler) SetMediaStatsGatherer(st *stats.LocalMediaStatsGatherer) {
	// No stats to gather
}

func (h *sdkWhipHandler) Close() {
	h.closed.Break()
}

func (h *sdkWhipHandler) WaitForSessionEnd(ctx context.Context) error {
	// TODO hang up control connection

	<-h.closed.Watch()

	// Leave may fail if we're disonnected
	h.client.SendLeave()
	h.client.Close()

	h.logger.Infow("WHIP handler ended")

	return nil
}

func (h *sdkWhipHandler) addTrackVideoTrack(cid string, source livekit.TrackSource, width uint32, height uint32) error {
	req := &livekit.AddTrackRequest{
		Cid:    cid,
		Source: source,
		Type:   livekit.TrackType_VIDEO,
		Width:  width,
		Height, height,
	}

	req.Layers = []*livekit.VideoLayer{
		{
			Quality: livekit.VideoQuality_HIGH,
			Width:   width,
			Height:  height,
		},
	}

	return h.addTrack(req)
}

func (h *sdkWhipHandler) addTrackVideoTrack(cid string, source livekit.TrackSource, disableDTX bool, stereo bool) error {
	req := &livekit.AddTrackRequest{
		Cid:        cid,
		Source:     source,
		Type:       livekit.TrackType_AUDIO,
		DisableDtx: disableDTX,
		Stereo:     stereo,
	}

	return h.addTrack(req)
}

func (h *sdkWhipHandler) addTrack(req *livekit.AddTrackRequest) error {
	err := h.client.SendRequest(&livekit.SignalRequest{
		Message: &livekit.SignalRequest_AddTrack{
			AddTrack: req,
		},
	})
	if err != nil {
		return nil, err
	}

	pubChan := p.engine.TrackPublishedChan()
	var pubRes *livekit.TrackPublishedResponse

	select {
	case pubRes = <-pubChan:
		break
	case <-time.After(trackPublishTimeout):
		return nil, ErrTrackPublishTimeout
	}
}

func (h *sdkWhipHandler) AssociateRelay(kind types.StreamKind, token string, w io.WriteCloser) error {
	return nil
}

func (h *sdkWhipHandler) DissociateRelay(kind types.StreamKind) {
}

func (h *sdkWhipHandler) reconnect() error {
	sleepDuration := time.Second

	var retryCount int
	for retryCount = 0; retryCount <= maxSignalRetryCount; retryCount++ {
		_, err := h.client.Reconnect(h.params.WsUrl, h.params.Token)
		if err == nil {
			h.logger.Infow("successfully reconnected to controller", "retryCount", retryCount)
			return nil
		}

		sleepDuration *= 2
		if sleepDuration > maxWaitTime {
			sleepDuration = maxWaitTime
		}
		select {
		case <-time.After(sleepDuration):
		case <-h.closed.Watch():
			return errors.ErrServerShuttingDown
		}
	}

	h.logger.Infow("failed reconnecting", "retryCount", retryCount)

	return errors.ErrRoomDisconnected
}
