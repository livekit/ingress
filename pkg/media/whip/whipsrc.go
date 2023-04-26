package whip

import (
	"bytes"
	"context"
	"net/http"
	"sync"

	"github.com/pion/ice/v2"
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/logger"
	pionlogger "github.com/livekit/protocol/logger/pion"
	"github.com/livekit/protocol/utils"
	"github.com/livekit/psrpc"
	"github.com/livekit/server-sdk-go/pkg/synchronizer"
)

// TODO STUN & TURN
// TODO pion log level
// TODO handle ICE never succeeding / data never coming
// TODO Cleanup

const (
	// TODO: 2 for audio and video
	WHIPAppSourceLabel   = "whipAppSrc"
	defaultUDPBufferSize = 16_777_216
	whipIdentity         = "WHIPIngress"
)

type WHIPSource struct {
	params *params.Params

	pc   *webrtc.PeerConnection
	sync *synchronizer.Synchronizer

	trackLock sync.Mutex
	tracks    map[string]*webrtc.TrackRemote
	trackSrc  map[types.StreamKind]*WHIPAppSource
}

func NewWHIPSource(ctx context.Context, p *params.Params) (*WHIPSource, error) {
	s := &WHIPSource{
		params: p,
		sync:   synchronizer.NewSynchronizer(nil),
	}

	logFactory := pionlogger.NewLoggerFactory(logger.GetLogger())
	webrtcSettings := &webrtc.SettingEngine{}

	sdpOffer := p.ExtraParams.(*params.WhipExtraParams).SDPOffer
	if p.Whip.ICESinglePort != 0 {
		logger.Infow("listen ice on single-port", "port", p.Whip.ICESinglePort)
		opts := []ice.UDPMuxFromPortOption{
			ice.UDPMuxFromPortWithReadBufferSize(defaultUDPBufferSize),
			ice.UDPMuxFromPortWithWriteBufferSize(defaultUDPBufferSize),
			ice.UDPMuxFromPortWithLogger(logFactory.NewLogger("udp_mux")),
		}
		if p.Whip.EnableLoopbackCandidate {
			opts = append(opts, ice.UDPMuxFromPortWithLoopback())
		}
		udpMux, err := ice.NewMultiUDPMuxFromPort(p.Whip.ICESinglePort, opts...)
		if err != nil {
			return nil, err
		}

		webrtcSettings.SetICEUDPMux(udpMux)
	} else {
		var icePortStart, icePortEnd uint16

		if len(p.Whip.ICEPortRange) == 2 {
			icePortStart = p.Whip.ICEPortRange[0]
			icePortEnd = p.Whip.ICEPortRange[1]
		}
		if icePortStart != 0 || icePortEnd != 0 {
			if err := webrtcSettings.SetEphemeralUDPPortRange(icePortStart, icePortEnd); err != nil {
				return nil, err
			}
		}
	}

	m, err := newMediaEngine(p)
	if err != nil {
		return nil, err
	}

	// Create a InterceptorRegistry. This is the user configurable RTP/RTCP Pipeline.
	// This provides NACKs, RTCP Reports and other features. If you use `webrtc.NewPeerConnection`
	// this is enabled by default. If you are manually managing You MUST create a InterceptorRegistry
	// for each PeerConnection.
	i := &interceptor.Registry{}

	// Use the default set of Interceptors
	if err := webrtc.RegisterDefaultInterceptors(m, i); err != nil {
		return nil, err
	}

	// Create the API object with the MediaEngine
	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithSettingEngine(*webrtcSettings), webrtc.WithInterceptorRegistry(i))
	s.pc, err = s.createPeerConnection(api)
	if err != nil {
		return nil, err
	}

	sdpAnswer, err := s.getSDPAnswer(ctx, sdpOffer)
	if err != nil {
		return nil, err
	}

	err = s.postSDPAnswer(ctx, sdpAnswer)
	if err != nil {
		return nil, err
	}

	return s, nil
}

func (s *WHIPSource) Start(ctx context.Context) error {
	for _, v := range s.trackSrc {
		err := v.Start()
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *WHIPSource) Close() error {
	s.sync.End()

	var errs utils.ErrArray
	for _, v := range s.trackSrc {
		err := v.Close()
		if err != nil {
			errs.AppendErr(err)
		}
	}

	return errs.ToError()
}

func (s *WHIPSource) GetSource(kind types.StreamKind) *app.Source {
	s.trackLock.Lock()
	defer s.trackLock.Unlock()

	src := s.trackSrc[kind]
	if src != nil {
		return src.GetAppSource()
	}

	return nil
}

func (s *WHIPSource) createPeerConnection(api *webrtc.API) (*webrtc.PeerConnection, error) {
	config := webrtc.Configuration{
		ICEServers:   []webrtc.ICEServer{},
		SDPSemantics: webrtc.SDPSemanticsUnifiedPlanWithFallback,
		BundlePolicy: webrtc.BundlePolicyBalanced,
	}

	// Create a new RTCPeerConnection
	pc, err := api.NewPeerConnection(config)
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

	pc.OnTrack(s.addTrack)

	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		logger.Infow("Peer Connection State has changed", "state", s.String())

		// TODO handle state change
	})

	return pc, nil
}

func (s *WHIPSource) getSDPAnswer(ctx context.Context, sdpOffer string) (string, error) {
	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  sdpOffer,
	}

	// Set the remote SessionDescription
	err := s.pc.SetRemoteDescription(offer)
	if err != nil {
		s.pc.Close()
		return "", err
	}

	// Create an answer
	answer, err := s.pc.CreateAnswer(nil)
	if err != nil {
		s.pc.Close()
		return "", err
	}

	// Create channel that is blocked until ICE Gathering is complete
	gatherComplete := webrtc.GatheringCompletePromise(s.pc)

	// Sets the LocalDescription, and starts our UDP listeners
	if err = s.pc.SetLocalDescription(answer); err != nil {
		s.pc.Close()
		return "", err
	}

	select {
	case <-gatherComplete:
		// success
	case <-ctx.Done():
		return "", psrpc.NewErrorf(psrpc.DeadlineExceeded, "timed out while waiting for ICE candidate gathering")
	}

	sdpAnswer := s.pc.LocalDescription().SDP

	return sdpAnswer, nil
}

func (s *WHIPSource) postSDPAnswer(ctx context.Context, sdpAnswer string) error {
	c := &http.Client{}
	req, err := http.NewRequestWithContext(ctx, "POST", s.params.RelayUrl, bytes.NewReader([]byte(sdpAnswer)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/sdp")

	resp, err := c.Do(req)
	switch {
	case err != nil:
		return psrpc.NewErrorf(psrpc.Internal, "failed connecting to relay")
	case resp.StatusCode != http.StatusOK:
		return psrpc.NewErrorf(psrpc.Internal, "failed response from relay")
	}

	return nil
}

func (s *WHIPSource) addTrack(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	logger.Infow("track has started", "type", track.PayloadType(), "codec", track.Codec().MimeType)

	s.trackLock.Lock()
	defer s.trackLock.Unlock()
	s.tracks[track.ID()] = track
	kind := streamKindFromCodecType(track.Kind())
	if _, ok := s.trackSrc[kind]; ok {
		logger.Warnw("duplicate track of the same kind", errors.ErrUnsupportedDecodeFormat, "kind", kind)
		return
	}

	sync := s.sync.AddTrack(track, whipIdentity)
	appSrc, err := NewWHIPAppSource(track, receiver, sync, s.writePLI, s.sync.OnRTCP)
	if err != nil {
		logger.Warnw("failed creating whip app source", err)
		return
	}
	s.trackSrc[kind] = appSrc
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

func (w *WHIPSource) writePLI(ssrc webrtc.SSRC) {
	pli := []rtcp.Packet{
		&rtcp.PictureLossIndication{SenderSSRC: uint32(ssrc), MediaSSRC: uint32(ssrc)},
	}
	err := w.pc.WriteRTCP(pli)
	if err != nil {
		logger.Warnw("failed writing PLI", err, "ssrc", ssrc)
	}
}

func newMediaEngine(p *params.Params) (*webrtc.MediaEngine, error) {
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
