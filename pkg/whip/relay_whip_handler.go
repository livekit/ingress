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
	"sync"
	"time"

	"github.com/pion/dtls/v2/pkg/crypto/elliptic"
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v3"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/ingress/pkg/utils"
	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
	"github.com/livekit/protocol/logger"
	putils "github.com/livekit/protocol/utils"
	"github.com/livekit/psrpc"
	"github.com/livekit/server-sdk-go/v2/pkg/synchronizer"
)

const (
	whipIdentity = "WHIPIngress"

	dtlsRetransmissionInterval = 100 * time.Millisecond
	maxRetryCount              = 3
)

type relayWhipHandler struct {
	logger logger.Logger
	params *params.Params

	rtcConfig          *rtcconfig.WebRTCConfig
	pc                 *webrtc.PeerConnection
	sync               *synchronizer.Synchronizer
	outputSync         *utils.OutputSynchronizer
	stats              *stats.LocalMediaStatsGatherer
	expectedTrackCount int
	closeOnce          sync.Once

	trackLock      sync.Mutex
	tracks         map[string]*webrtc.TrackRemote
	trackHandlers  map[types.StreamKind]*RelayWhipTrackHandler
	trackAddedChan chan *webrtc.TrackRemote
}

func NewRelayWhipHandler(webRTCConfig *rtcconfig.WebRTCConfig) *relayWhipHandler {
	// Copy the rtc conf to allow modifying to to match the request
	rtcConfCopy := *webRTCConfig

	return &relayWhipHandler{
		rtcConfig:     &rtcConfCopy,
		sync:          synchronizer.NewSynchronizer(nil),
		tracks:        make(map[string]*webrtc.TrackRemote),
		trackHandlers: make(map[types.StreamKind]*RelayWhipTrackHandler),
	}
}

func (h *relayWhipHandler) Init(ctx context.Context, p *params.Params, sdpOffer string) (string, error) {
	var err error

	h.logger = p.GetLogger()
	h.params = p

	h.updateSettings()

	offer := &webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  sdpOffer,
	}

	h.expectedTrackCount, err = h.validateOfferAndGetExpectedTrackCount(offer)
	if err != nil {
		return "", err
	}

	h.trackAddedChan = make(chan *webrtc.TrackRemote, h.expectedTrackCount)

	m, err := newMediaEngine()
	if err != nil {
		return "", err
	}

	// Create a InterceptorRegistry. This is the user configurable RTP/RTCP Pipeline.
	// This provides NACKs, RTCP Reports and other features. If you use `webrtc.NewPeerConnection`
	// this is enabled by default. If you are manually managing You MUST create a InterceptorRegistry
	// for each PeerConnection.
	i := &interceptor.Registry{}

	// Use the default set of Interceptors
	if err := webrtc.RegisterDefaultInterceptors(m, i); err != nil {
		return "", err
	}

	// Create the API object with the MediaEngine
	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithSettingEngine(h.rtcConfig.SettingEngine), webrtc.WithInterceptorRegistry(i))
	h.pc, err = h.createPeerConnection(api)
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			h.pc.Close()
		}
	}()

	sdpAnswer, err := h.getSDPAnswer(ctx, offer)
	if err != nil {
		return "", err
	}

	return sdpAnswer, nil
}

func (h *relayWhipHandler) Start(ctx context.Context) (map[types.StreamKind]string, error) {
	var trackCount int
	mimeTypes := make(map[types.StreamKind]string)

loop:
	for {
		select {
		case <-ctx.Done():
			return nil, errors.ErrSourceNotReady
		case track := <-h.trackAddedChan:
			mimeTypes[streamKindFromCodecType(track.Kind())] = track.Codec().MimeType

			trackCount++
			if trackCount == h.expectedTrackCount {
				break loop
			}
		}
	}

	h.trackLock.Lock()
	defer h.trackLock.Unlock()

	return mimeTypes, nil
}

func (h *relayWhipHandler) SetMediaStatsGatherer(st *stats.LocalMediaStatsGatherer) {
	h.trackLock.Lock()
	defer h.trackLock.Unlock()
	h.stats = st

	for _, th := range h.trackHandlers {
		th.SetMediaTrackStatsGatherer(st)
	}
}

func (h *relayWhipHandler) Close() {
	if h.pc != nil {
		h.pc.Close()
	}
}

func (h *relayWhipHandler) WaitForSessionEnd(ctx context.Context) error {
	defer func() {
		h.logger.Infow("closing peer connection")
		h.pc.Close()
	}()

	var err error
	for retryCount := 0; retryCount < maxRetryCount; retryCount++ {
		err = h.runSession(ctx)

		var retrError errors.RetryableError
		if err == nil || !errors.As(err, &retrError) {
			return err
		}

		h.logger.Infow("whip session failed with retryable error", "error", err)
	}

	return err
}

func (h *relayWhipHandler) AssociateRelay(kind types.StreamKind, token string, w io.WriteCloser) error {
	h.trackLock.Lock()
	defer h.trackLock.Unlock()

	if token != h.params.RelayToken {
		return errors.ErrInvalidRelayToken
	}

	th, ok := h.trackHandlers[kind]
	if !ok {
		h.logger.Errorw("track handler not found", nil)
		return errors.ErrIngressNotFound
	}

	err := th.SetWriter(w)
	if err != nil {
		return err
	}

	return nil
}

func (h *relayWhipHandler) DissociateRelay(kind types.StreamKind) {
	h.trackLock.Lock()
	defer h.trackLock.Unlock()

	th, ok := h.trackHandlers[kind]
	if !ok {
		return
	}

	err := th.SetWriter(nil)
	if err != nil {
		return
	}
}

func (h *relayWhipHandler) updateSettings() {
	se := &h.rtcConfig.SettingEngine

	// Change elliptic curve to improve connectivity
	// https://github.com/pion/dtls/pull/474
	se.SetDTLSEllipticCurves(elliptic.X25519, elliptic.P384, elliptic.P256)

	//
	// Disable SRTP replay protection (https://datatracker.ietf.org/doc/html/rfc3711#page-15).
	// Needed due to lack of RTX stream support in Pion.
	//
	// When clients probe for bandwidth, there are several possible approaches
	//   1. Use padding packet (Chrome uses this)
	//   2. Use an older packet (Firefox uses this)
	// Typically, these are sent over the RTX stream and hence SRTP replay protection will not
	// trigger. As Pion does not support RTX, when firefox uses older packet for probing, they
	// trigger the replay protection.
	//
	// That results in two issues
	//   - Firefox bandwidth probing is not successful
	//   - Pion runs out of read buffer capacity - this potentially looks like a Pion issue
	//
	// NOTE: It is not required to disable RTCP replay protection, but doing it to be symmetric.
	//
	se.DisableSRTPReplayProtection(true)
	se.DisableSRTCPReplayProtection(true)
	se.SetDTLSRetransmissionInterval(dtlsRetransmissionInterval)
}

func (h *relayWhipHandler) createPeerConnection(api *webrtc.API) (*webrtc.PeerConnection, error) {
	// Create a new RTCPeerConnection
	pc, err := api.NewPeerConnection(h.rtcConfig.Configuration)
	if err != nil {
		return nil, err
	}

	// Accept one audio and one video track incoming
	for _, typ := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeAudio} {
		if _, err := pc.AddTransceiverFromKind(typ, webrtc.RTPTransceiverInit{
			Direction: webrtc.RTPTransceiverDirectionRecvonly,
		}); err != nil {
			return nil, err
		}
	}

	pc.OnTrack(h.addTrack)

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		h.logger.Infow("Peer Connection State changed", "state", state.String())

		if state >= webrtc.PeerConnectionStateDisconnected {
			h.closeOnce.Do(func() {
				h.sync.End()

				h.trackLock.Lock()
				for _, v := range h.trackHandlers {
					v.Close()
				}
				h.trackLock.Unlock()
			})
		}
	})

	return pc, nil
}

func (h *relayWhipHandler) getSDPAnswer(ctx context.Context, offer *webrtc.SessionDescription) (string, error) {
	// Set the remote SessionDescription
	err := h.pc.SetRemoteDescription(*offer)
	if err != nil {
		return "", err
	}

	// Create an answer
	answer, err := h.pc.CreateAnswer(nil)
	if err != nil {
		return "", err
	}

	// Create channel that is blocked until ICE Gathering is complete
	gatherComplete := webrtc.GatheringCompletePromise(h.pc)

	// Sets the LocalDescription, and starts our UDP listeners
	if err = h.pc.SetLocalDescription(answer); err != nil {
		return "", err
	}

	select {
	case <-gatherComplete:
		// success
	case <-ctx.Done():
		return "", psrpc.NewErrorf(psrpc.DeadlineExceeded, "timed out while waiting for ICE candidate gathering")
	}

	parsedAnswer, err := h.pc.LocalDescription().Unmarshal()
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

	sdpAnswer := h.pc.LocalDescription().SDP

	return sdpAnswer, nil
}

func (h *relayWhipHandler) addTrack(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	kind := streamKindFromCodecType(track.Kind())
	logger := h.logger.WithValues("trackID", track.ID(), "kind", kind)

	logger.Infow("track has started", "type", track.PayloadType(), "codec", track.Codec().MimeType)

	h.trackLock.Lock()
	defer h.trackLock.Unlock()
	h.tracks[track.ID()] = track

	sync := h.sync.AddTrack(track, whipIdentity)

	th, err := NewRelayWhipTrackHandler(logger, track, sync, receiver, h.writePLI, h.sync.OnRTCP)
	if err != nil {
		logger.Warnw("failed creating relay whip track handler", err)
		return
	}
	h.trackHandlers[kind] = th

	select {
	case h.trackAddedChan <- track:
	default:
		logger.Warnw("failed notifying of new track", errors.New("channel full"))
	}
}

func (h *relayWhipHandler) writePLI(ssrc webrtc.SSRC) {
	h.logger.Debugw("sending PLI request", "ssrc", ssrc)
	pli := []rtcp.Packet{
		&rtcp.PictureLossIndication{SenderSSRC: uint32(ssrc), MediaSSRC: uint32(ssrc)},
	}
	err := h.pc.WriteRTCP(pli)
	if err != nil {
		h.logger.Warnw("failed writing PLI", err, "ssrc", ssrc)
	}
}

func (h *relayWhipHandler) runSession(ctx context.Context) error {
	var err error

	result := make(chan error, 1)

	for _, th := range h.trackHandlers {
		err := th.Start(func(err error) {
			result <- err
		})
		if err != nil {
			return err
		}
	}

	var trackDoneCount int
	var errs putils.ErrArray

loop:
	for {
		select {
		case <-ctx.Done():
			return errors.ErrSourceNotReady
		case resErr := <-result:
			trackDoneCount++
			if resErr != nil {
				errs.AppendErr(resErr)
			}
			if trackDoneCount == h.expectedTrackCount {
				err = errs.ToError()
				break loop
			}
		}
	}

	return err
}

func (h *relayWhipHandler) validateOfferAndGetExpectedTrackCount(offer *webrtc.SessionDescription) (int, error) {
	parsed, err := offer.Unmarshal()
	if err != nil {
		return 0, err
	}

	audioCount, videoCount := 0, 0

	for _, m := range parsed.MediaDescriptions {
		if types.StreamKind(m.MediaName.Media) == types.Audio {
			// Duplicate track for a given type. Forbidden by the RFC
			if audioCount != 0 {
				return 0, errors.ErrDuplicateTrack
			}

			audioCount++

		} else if types.StreamKind(m.MediaName.Media) == types.Video {
			// Duplicate track for a given type. Forbidden by the RFC
			if videoCount != 0 {
				return 0, errors.ErrDuplicateTrack
			}

			for _, a := range m.Attributes {
				if a.Key == "simulcast" {
					return 0, errors.ErrSimulcastTranscode
				}
			}

			videoCount++
		}
	}

	return audioCount + videoCount, nil
}

func streamKindFromCodecType(typ webrtc.RTPCodecType) types.StreamKind {
	switch typ {
	case webrtc.RTPCodecTypeAudio:
		return types.Audio
	case webrtc.RTPCodecTypeVideo:
		return types.Video
	default:
		return types.Unknown
	}
}

func newMediaEngine() (*webrtc.MediaEngine, error) {
	m := &webrtc.MediaEngine{}

	for _, codec := range []webrtc.RTPCodecParameters{
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMA, ClockRate: 8000},
			PayloadType:        8,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: "minptime=10;useinbandfec=1", RTCPFeedback: nil},
			PayloadType:        111,
		},
	} {
		if err := m.RegisterCodec(codec, webrtc.RTPCodecTypeAudio); err != nil {
			return nil, err
		}
	}

	videoRTCPFeedback := []webrtc.RTCPFeedback{{Type: "goog-remb", Parameter: ""}, {Type: "ccm", Parameter: "fir"}, {Type: "nack", Parameter: ""}, {Type: "nack", Parameter: "pli"}}

	for _, codec := range []webrtc.RTPCodecParameters{
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000, RTCPFeedback: videoRTCPFeedback},
			PayloadType:        96,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f", RTCPFeedback: videoRTCPFeedback},
			PayloadType:        102,
		},
	} {
		if err := m.RegisterCodec(codec, webrtc.RTPCodecTypeVideo); err != nil {
			return nil, err
		}
	}

	if err := m.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: sdp.SDESMidURI}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}

	if err := m.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: sdp.SDESRTPStreamIDURI}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}

	return m, nil
}
