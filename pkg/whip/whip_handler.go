package whip

import (
	"context"
	"io"
	"sync"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
	google_protobuf2 "google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/lksdk_output"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/tracer"
	"github.com/livekit/protocol/utils"
	"github.com/livekit/psrpc"
	"github.com/livekit/server-sdk-go/pkg/synchronizer"
)

const (
	whipIdentity = "WHIPIngress"
)

// TODO log ingress id / resource ID

type whipHandler struct {
	logger logger.Logger
	params *params.Params

	rtcConfig          *rtcconfig.WebRTCConfig
	pc                 *webrtc.PeerConnection
	sync               *synchronizer.Synchronizer
	sdkOutput          *lksdk_output.LKSDKOutput // only for passthrough
	expectedTrackCount int
	result             chan error
	closeOnce          sync.Once

	trackLock           sync.Mutex
	tracks              map[string]*webrtc.TrackRemote
	trackHandlers       map[types.StreamKind]*whipTrackHandler
	trackRelayMediaSink map[types.StreamKind]*RelayMediaSink // only for transcoding mode
	trackAddedChan      chan *webrtc.TrackRemote
}

func NewWHIPHandler(webRTCConfig *rtcconfig.WebRTCConfig) *whipHandler {
	return &whipHandler{
		rtcConfig:           webRTCConfig,
		sync:                synchronizer.NewSynchronizer(nil),
		result:              make(chan error, 1),
		tracks:              make(map[string]*webrtc.TrackRemote),
		trackHandlers:       make(map[types.StreamKind]*whipTrackHandler),
		trackRelayMediaSink: make(map[types.StreamKind]*RelayMediaSink),
	}
}

func (h *whipHandler) Init(ctx context.Context, p *params.Params, sdpOffer string) (string, error) {
	var err error

	h.logger = logger.GetLogger().WithValues("ingressID", p.IngressId, "resourceID", p.State.ResourceId)
	h.params = p

	if p.BypassTranscoding {
		h.sdkOutput, err = lksdk_output.NewLKSDKOutput(ctx, p)
		if err != nil {
			return "", err
		}
	}

	offer := &webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  sdpOffer,
	}
	h.expectedTrackCount, err = validateOfferAndGetExpectedTrackCount(offer)
	h.trackAddedChan = make(chan *webrtc.TrackRemote, h.expectedTrackCount)
	if err != nil {
		return "", err
	}

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
	for _, th := range h.trackHandlers {
		err := th.Start(func(err error) {
			h.result <- err
		})
		if err != nil {
			return nil, err
		}
	}

	return mimeTypes, nil
}

func (h *whipHandler) Close() {
	if h.pc != nil {
		h.pc.Close()
	}
}

func (h *whipHandler) WaitForSessionEnd(ctx context.Context) error {
	defer func() {
		h.logger.Infow("closing peer connection")
		h.pc.Close()
		if h.sdkOutput != nil {
			h.sdkOutput.Close()
		}
	}()

	var trackDoneCount int
	var errs utils.ErrArray

	for {
		select {
		case <-ctx.Done():
			return errors.ErrSourceNotReady
		case err := <-h.result:
			trackDoneCount++
			if err != nil {
				errs.AppendErr(err)
			}
			if trackDoneCount == h.expectedTrackCount {
				return errs.ToError()
			}
		}
	}
}

func (h *whipHandler) AssociateRelay(kind types.StreamKind, w io.WriteCloser) error {
	h.trackLock.Lock()
	defer h.trackLock.Unlock()
	th := h.trackRelayMediaSink[kind]
	if th == nil {
		return errors.ErrIngressNotFound
	}

	err := th.SetWriter(w)
	if err != nil {
		return err
	}

	return nil
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

		// TODO support ICE Restart
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
	if len(parsedAnswer.MediaDescriptions) != h.expectedTrackCount {
		return "", errors.ErrUnsupportedDecodeFormat
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

func (h *whipHandler) addTrack(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	kind := streamKindFromCodecType(track.Kind())
	logger := h.logger.WithValues("trackID", track.ID(), "kind", kind)

	logger.Infow("track has started", "type", track.PayloadType(), "codec", track.Codec().MimeType)

	h.trackLock.Lock()
	defer h.trackLock.Unlock()
	h.tracks[track.ID()] = track

	sync := h.sync.AddTrack(track, whipIdentity)

	mediaSink, err := h.newMediaSink(track)
	if err != nil {
		logger.Warnw("failed creating whip  media handler", err)
		return
	}

	th, err := newWHIPTrackHandler(logger, track, receiver, sync, mediaSink, h.writePLI, h.sync.OnRTCP)
	if err != nil {
		logger.Warnw("failed creating whip track handler", err)
		return
	}
	h.trackHandlers[kind] = th

	select {
	case h.trackAddedChan <- track:
	default:
		logger.Warnw("failed notifying of new track", errors.New("channel full"))
	}
}

func (h *whipHandler) newMediaSink(track *webrtc.TrackRemote) (MediaSink, error) {
	if h.sdkOutput != nil {
		// pasthrough
		return NewSDKMediaSink(h.logger, h.params, h.sdkOutput, track, func() {
			h.writePLI(track.SSRC())
		}), nil
	} else {
		kind := streamKindFromCodecType(track.Kind())

		s := NewRelayMediaSink(h.logger.WithValues("trackID", track.ID(), "kind", kind))

		h.trackRelayMediaSink[kind] = s
		return s, nil
	}
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

func validateOfferAndGetExpectedTrackCount(offer *webrtc.SessionDescription) (int, error) {
	parsed, err := offer.Unmarshal()
	if err != nil {
		return 0, err
	}

	mediaTypes := make(map[string]struct{})
	for _, m := range parsed.MediaDescriptions {
		if _, ok := mediaTypes[m.MediaName.Media]; ok {
			// Duplicate track for a given type. Forbidden by the RFC
			return 0, errors.ErrDuplicateTrack
		}
		mediaTypes[m.MediaName.Media] = struct{}{}
	}

	return len(parsed.MediaDescriptions), nil
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

	videoRTCPFeedback := []webrtc.RTCPFeedback{{"goog-remb", ""}, {"ccm", "fir"}, {"nack", ""}, {"nack", "pli"}}

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
