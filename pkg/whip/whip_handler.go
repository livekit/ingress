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
	"bufio"
	"context"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/pion/dtls/v3/pkg/crypto/elliptic"
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
	google_protobuf2 "google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/lksdk_output"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/ingress/pkg/utils"
	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/logger/pionlogger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/tracer"
	putils "github.com/livekit/protocol/utils"
	"github.com/livekit/psrpc"
	"github.com/livekit/server-sdk-go/v2/pkg/synchronizer"
)

const (
	whipIdentity = "WHIPIngress"

	dtlsRetransmissionInterval = 100 * time.Millisecond
	maxRetryCount              = 3
)

var (
	ignoredLogPrefixes = map[string][]string{
		"ice": {
			"Failed to ping without candidate pairs",
		},
	}
)

type WhipTrackHandler interface {
	Start(onDone func(err error)) (err error)
	Close()
	SetMediaTrackStatsGatherer(g *stats.LocalMediaStatsGatherer)
}

type WhipTrackDescription struct {
	Kind    types.StreamKind
	Quality livekit.VideoQuality
}

type whipHandler struct {
	logger    logger.Logger
	params    *params.Params
	rpcServer rpc.IngressHandlerServer

	rtcConfig          *rtcconfig.WebRTCConfig
	pc                 *webrtc.PeerConnection
	sync               *synchronizer.Synchronizer
	stats              *stats.LocalMediaStatsGatherer
	expectedTrackCount int
	closeOnce          sync.Once

	trackLock       sync.Mutex
	simulcastLayers []string
	tracks          []*webrtc.TrackRemote
	trackHandlers   map[WhipTrackDescription]WhipTrackHandler
	trackAddedChan  chan *webrtc.TrackRemote

	trackSDKMediaSinkLock sync.Mutex
	trackSDKMediaSink     map[types.StreamKind]*SDKMediaSink
}

func NewWHIPHandler(p *params.Params, webRTCConfig *rtcconfig.WebRTCConfig, bus psrpc.MessageBus) (*whipHandler, error) {
	// Copy the rtc conf to allow modifying to to match the request
	rtcConfCopy := *webRTCConfig

	h := &whipHandler{
		rtcConfig:         &rtcConfCopy,
		params:            p,
		logger:            p.GetLogger(),
		sync:              synchronizer.NewSynchronizer(nil),
		trackHandlers:     make(map[WhipTrackDescription]WhipTrackHandler),
		trackSDKMediaSink: make(map[types.StreamKind]*SDKMediaSink),
	}

	var err error
	if bus != nil {
		h.rpcServer, err = rpc.NewIngressHandlerServer(h, bus)
		if err != nil {
			return nil, err
		}
		err = utils.RegisterIngressRpcHandlers(h.rpcServer, p.IngressInfo)
		if err != nil {
			return nil, err
		}
	}

	return h, nil
}

func (h *whipHandler) Init(ctx context.Context, sdpOffer string) (string, error) {
	var err error

	h.updateSettings()

	offer := &webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  sdpOffer,
	}

	h.expectedTrackCount, err = h.validateOfferAndGetExpectedTrackCount(offer)
	if err != nil {
		return "", err
	}

	if *h.params.EnableTranscoding && len(h.simulcastLayers) != 0 {
		return "", errors.ErrSimulcastTranscode
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

	if *h.params.EnableTranscoding {
		// Use the default set of Interceptors
		if err := webrtc.RegisterDefaultInterceptors(m, i); err != nil {
			return "", err
		}
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

func (h *whipHandler) Start(ctx context.Context) (map[types.StreamKind]string, error) {
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

func (h *whipHandler) SetMediaStatsGatherer(st *stats.LocalMediaStatsGatherer) {
	h.trackLock.Lock()
	defer h.trackLock.Unlock()
	h.stats = st

	for _, th := range h.trackHandlers {
		th.SetMediaTrackStatsGatherer(st)
	}
}

func (h *whipHandler) Close() {
	if h.rpcServer != nil {
		utils.DeregisterIngressRpcHandlers(h.rpcServer, h.params.IngressInfo)
	}
	if h.pc != nil {
		h.pc.Close()
	}
}

func (h *whipHandler) WaitForSessionEnd(ctx context.Context) error {
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

func (h *whipHandler) AssociateRelay(kind types.StreamKind, token string, w io.WriteCloser) error {
	h.trackLock.Lock()
	defer h.trackLock.Unlock()

	if token != h.params.RelayToken {
		return errors.ErrInvalidRelayToken
	}

	t, ok := h.trackHandlers[WhipTrackDescription{Kind: kind, Quality: livekit.VideoQuality_HIGH}]
	if !ok {
		h.logger.Errorw("track handler not found", nil)
		return errors.ErrIngressNotFound
	}

	th, ok := t.(*RelayWhipTrackHandler)
	if !ok {
		h.logger.Errorw("failed type assertion on track handler", nil)
		return errors.ErrIngressNotFound
	}

	err := th.SetWriter(w)
	if err != nil {
		return err
	}

	return nil
}

func (h *whipHandler) DissociateRelay(kind types.StreamKind) {
	h.trackLock.Lock()
	defer h.trackLock.Unlock()

	t, ok := h.trackHandlers[WhipTrackDescription{Kind: kind, Quality: livekit.VideoQuality_HIGH}]
	if !ok {
		return
	}

	th, ok := t.(*RelayWhipTrackHandler)
	if !ok {
		h.logger.Errorw("failed type assertion on track handler", nil)
		return
	}

	err := th.SetWriter(nil)
	if err != nil {
		return
	}
}

func (h *whipHandler) updateSettings() {
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

	o := pionlogger.WithPrefixFilter(pionlogger.NewPrefixFilter(ignoredLogPrefixes))

	se.LoggerFactory = pionlogger.NewLoggerFactory(h.logger, o)
}

func (h *whipHandler) createPeerConnection(api *webrtc.API) (*webrtc.PeerConnection, error) {
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

		if state >= webrtc.PeerConnectionStateFailed {
			h.closeOnce.Do(func() {
				h.sync.End()

				h.closeTrackHandlers()
			})
		}
	})

	return pc, nil
}

func (h *whipHandler) getSDPAnswer(ctx context.Context, offer *webrtc.SessionDescription) (string, error) {
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

func (h *whipHandler) getTrackQuality(track *webrtc.TrackRemote) livekit.VideoQuality {
	trackQuality := livekit.VideoQuality_HIGH
	if track.RID() != "" {
		for i, expectedRid := range h.simulcastLayers {
			if expectedRid == track.RID() {
				switch i {
				case 1:
					trackQuality = livekit.VideoQuality_MEDIUM
				case 2:
					trackQuality = livekit.VideoQuality_LOW
				}
			}
		}
	}

	return trackQuality
}

func (h *whipHandler) addTrack(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	kind := streamKindFromCodecType(track.Kind())
	logger := h.logger.WithValues("trackID", track.ID(), "kind", kind)

	logger.Infow("track has started", "type", track.PayloadType(), "codec", track.Codec().MimeType)

	h.trackLock.Lock()
	defer h.trackLock.Unlock()
	h.tracks = append(h.tracks, track)

	trackQuality := h.getTrackQuality(track)

	var th WhipTrackHandler
	var err error
	if !*h.params.EnableTranscoding {
		th, err = NewSDKWhipTrackHandler(logger, track, trackQuality, receiver, h.writePLI, h.writeRTCPUpstream)
		if err != nil {
			logger.Warnw("failed creating SDK whip track handler", err)
			return
		}
	} else {
		sync := h.sync.AddTrack(track, whipIdentity)

		th, err = NewRelayWhipTrackHandler(logger, track, trackQuality, sync, receiver, h.writePLI, h.sync.OnRTCP)
		if err != nil {
			logger.Warnw("failed creating relay whip track handler", err)
			return
		}
	}
	h.trackHandlers[WhipTrackDescription{Kind: kind, Quality: trackQuality}] = th

	select {
	case h.trackAddedChan <- track:
	default:
		logger.Warnw("failed notifying of new track", errors.New("channel full"))
	}
}

func (h *whipHandler) getSDKTrackMediaSink(sdkOutput *lksdk_output.LKSDKOutput, track *webrtc.TrackRemote, trackQuality livekit.VideoQuality) (*SDKMediaSinkTrack, error) {
	kind := streamKindFromCodecType(track.Kind())

	h.trackSDKMediaSinkLock.Lock()
	defer h.trackSDKMediaSinkLock.Unlock()

	if _, ok := h.trackSDKMediaSink[kind]; !ok {
		layers := []livekit.VideoQuality{livekit.VideoQuality_HIGH}
		if kind == types.Video && len(h.simulcastLayers) == 3 {
			layers = []livekit.VideoQuality{livekit.VideoQuality_HIGH, livekit.VideoQuality_MEDIUM, livekit.VideoQuality_LOW}
		} else if kind == types.Video && len(h.simulcastLayers) == 2 {
			layers = []livekit.VideoQuality{livekit.VideoQuality_HIGH, livekit.VideoQuality_MEDIUM}
		}

		h.trackSDKMediaSink[kind] = NewSDKMediaSink(h.logger, h.params, sdkOutput, track.Codec(), streamKindFromCodecType(track.Kind()), layers)
	}

	sdkTrack := h.trackSDKMediaSink[kind].GetTrack(trackQuality)
	if sdkTrack == nil {
		err := errors.ErrIngressNotFound
		h.logger.Warnw("no SDK track for the current quality", err)
		return nil, err
	}

	return sdkTrack, nil
}

func (h *whipHandler) writePLI(ssrc webrtc.SSRC) {
	h.logger.Debugw("sending PLI request", "ssrc", ssrc)
	pli := []rtcp.Packet{
		&rtcp.PictureLossIndication{SenderSSRC: uint32(ssrc), MediaSSRC: uint32(ssrc)},
	}
	err := h.pc.WriteRTCP(pli)
	if err != nil {
		h.logger.Warnw("failed writing PLI", err, "ssrc", ssrc)
	}
}

func (h *whipHandler) writeRTCPUpstream(pkt rtcp.Packet) {
	err := h.pc.WriteRTCP([]rtcp.Packet{pkt})
	if err != nil {
		h.logger.Warnw("failed writing RTCP packet upstream", err)
	}
}

func (h *whipHandler) runSession(ctx context.Context) error {
	var err error
	var sdkOutput *lksdk_output.LKSDKOutput

	if !*h.params.EnableTranscoding {
		sdkOutput, err = lksdk_output.NewLKSDKOutput(ctx, nil, h.params)
		if err != nil {
			return err
		}

		h.trackLock.Lock()
		for _, track := range h.tracks {
			trackQuality := h.getTrackQuality(track)

			mediaSink, err := h.getSDKTrackMediaSink(sdkOutput, track, trackQuality)
			if err != nil {
				h.logger.Warnw("failed creating whip media handler", err)
				h.trackLock.Unlock()
				return err
			}

			t := h.trackHandlers[WhipTrackDescription{streamKindFromCodecType(track.Kind()), trackQuality}]
			th, ok := t.(*SDKWhipTrackHandler)
			if !ok {
				h.logger.Errorw("wrong type for track handler", errors.ErrIngressNotFound)
				return errors.ErrIngressNotFound
			}
			th.SetMediaSink(mediaSink)
		}
		h.trackLock.Unlock()
	}

	result := make(chan error, 1)

	h.trackLock.Lock()
	for td, th := range h.trackHandlers {
		err := th.Start(func(err error) {
			h.logger.Infow("track handler done", "error", err, "kind", td.Kind, "quality", td.Quality)
			// cancel all remaining track handlers
			h.closeTrackHandlers()

			result <- err
		})
		if err != nil {
			return err
		}
	}
	h.trackLock.Unlock()

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

	h.trackSDKMediaSinkLock.Lock()
	h.trackSDKMediaSink = make(map[types.StreamKind]*SDKMediaSink)
	h.trackSDKMediaSinkLock.Unlock()

	if sdkOutput != nil {
		sdkErr := sdkOutput.Close()
		if sdkErr != nil {
			// Output error takes precedence
			err = sdkErr
		}
	}

	return err
}

func (h *whipHandler) closeTrackHandlers() {
	h.trackLock.Lock()
	defer h.trackLock.Unlock()

	for _, th := range h.trackHandlers {
		th.Close()
	}
}

func (h *whipHandler) validateOfferAndGetExpectedTrackCount(offer *webrtc.SessionDescription) (int, error) {
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
					spaceSplit := strings.Split(a.Value, " ")
					if len(spaceSplit) != 2 || spaceSplit[0] != "send" {
						return 0, errors.ErrInvalidSimulcast
					}

					layersSplit := strings.Split(spaceSplit[1], ";")
					if len(layersSplit) != 2 && len(layersSplit) != 3 {
						return 0, errors.ErrInvalidSimulcast
					}

					h.simulcastLayers = layersSplit
					videoCount += len(h.simulcastLayers)
				}
			}

			// No Simulcast
			if videoCount == 0 {
				videoCount++
			}
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

// IngressHandler RPC interface
func (h *whipHandler) UpdateIngress(ctx context.Context, req *livekit.UpdateIngressRequest) (*livekit.IngressState, error) {
	_, span := tracer.Start(ctx, "whipHandler.UpdateIngress")
	defer span.End()

	h.Close()

	return h.params.CopyInfo().State, nil
}

func (h *whipHandler) DeleteIngress(ctx context.Context, req *livekit.DeleteIngressRequest) (*livekit.IngressState, error) {
	_, span := tracer.Start(ctx, "whipHandler.DeleteIngress")
	defer span.End()

	h.Close()

	return h.params.CopyInfo().State, nil
}

func (h *whipHandler) DeleteWHIPResource(ctx context.Context, req *rpc.DeleteWHIPResourceRequest) (*google_protobuf2.Empty, error) {
	_, span := tracer.Start(ctx, "whipHandler.DeleteWHIPResource")
	defer span.End()

	// only test for stream key correctness if it is part of the request for backward compatibility
	if req.StreamKey != "" && h.params.StreamKey != req.StreamKey {
		h.logger.Infow("received delete request with wrong stream key", "streamKey", req.StreamKey)
	}

	h.Close()

	return &google_protobuf2.Empty{}, nil
}

func (h *whipHandler) ICERestartWHIPResource(ctx context.Context, req *rpc.ICERestartWHIPResourceRequest) (*rpc.ICERestartWHIPResourceResponse, error) {
	_, span := tracer.Start(ctx, "whipHandler.ICERestartWHIPResource")
	defer span.End()

	if req.IfMatch != "*" {
		h.logger.Infow("WHIP client attempted Trickle-ICE")
		return nil, psrpc.NewErrorf(psrpc.UnprocessableEntity, "Trickle-ICE not supported")
	}

	if h.pc == nil {
		return nil, errors.ErrIngressNotFound
	}

	remoteDescription := h.pc.CurrentRemoteDescription()
	if remoteDescription == nil {
		return nil, errors.ErrIngressNotFound
	}

	// Replace the current remote description with the values from remote
	newRemoteDescription, err := replaceICEDetails(remoteDescription.SDP, req.UserFragment, req.Password)
	if err != nil {
		return nil, errors.ErrIngressNotFound
	}
	remoteDescription.SDP = newRemoteDescription

	if err := h.pc.SetRemoteDescription(*remoteDescription); err != nil {
		return nil, errors.ErrIngressNotFound
	}

	answer, err := h.pc.CreateAnswer(nil)
	if err != nil {
		return nil, errors.ErrIngressNotFound
	}

	gatherComplete := webrtc.GatheringCompletePromise(h.pc)
	if err = h.pc.SetLocalDescription(answer); err != nil {
		return nil, errors.ErrIngressNotFound
	}
	<-gatherComplete

	// Discard all `a=` lines that aren't ICE related
	// "WHIP does not support renegotiation of non-ICE related SDP information"
	//
	// https://www.ietf.org/archive/id/draft-ietf-wish-whip-14.html#name-ice-restarts
	var trickleIceSdpfrag strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(h.pc.LocalDescription().SDP))
	for scanner.Scan() {
		l := scanner.Text()
		if strings.HasPrefix(l, "a=") && !strings.HasPrefix(l, "a=ice-pwd") && !strings.HasPrefix(l, "a=ice-ufrag") && !strings.HasPrefix(l, "a=candidate") {
			continue
		}

		trickleIceSdpfrag.WriteString(l + "\n")
	}

	etag := fmt.Sprintf("%08x", crc32.ChecksumIEEE([]byte(trickleIceSdpfrag.String())))

	return &rpc.ICERestartWHIPResourceResponse{TrickleIceSdpfrag: trickleIceSdpfrag.String(), Etag: etag}, nil
}

func (h *whipHandler) WHIPRTCConnectionNotify(ctx context.Context, req *rpc.WHIPRTCConnectionNotifyRequest) (*google_protobuf2.Empty, error) {
	return &google_protobuf2.Empty{}, nil
}
