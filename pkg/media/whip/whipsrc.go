package whip

import (
	"bytes"
	"context"
	"net/http"
	"sync"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/logger/pionlogger"
	"github.com/livekit/protocol/utils"
	"github.com/livekit/psrpc"
	"github.com/livekit/server-sdk-go/pkg/synchronizer"
)

// TODO STUN & TURN
// TODO pion log level
// TODO handle ICE never succeeding / data never coming

const (
	WHIPAppSourceLabel   = "whipAppSrc"
	defaultUDPBufferSize = 16_777_216
	whipIdentity         = "WHIPIngress"
)

type WHIPSource struct {
	params *params.Params

	pc                 *webrtc.PeerConnection
	sync               *synchronizer.Synchronizer
	expectedTrackCount int
	result             chan error

	trackLock      sync.Mutex
	tracks         map[string]*webrtc.TrackRemote
	trackSrc       map[types.StreamKind]*WHIPAppSource
	trackAddedChan chan *webrtc.TrackRemote
}

func NewWHIPSource(ctx context.Context, p *params.Params) (*WHIPSource, error) {
	var err error

	s := &WHIPSource{
		params:   p,
		sync:     synchronizer.NewSynchronizer(nil),
		result:   make(chan error, 1),
		tracks:   make(map[string]*webrtc.TrackRemote),
		trackSrc: make(map[types.StreamKind]*WHIPAppSource),
	}

	offer := &webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  p.ExtraParams.(*params.WhipExtraParams).SDPOffer,
	}
	s.expectedTrackCount, err = getExpectedTrackCount(offer)
	s.trackAddedChan = make(chan *webrtc.TrackRemote, s.expectedTrackCount)
	if err != nil {
		return nil, err
	}

	webrtcSettings := &webrtc.SettingEngine{
		LoggerFactory: pionlogger.NewLoggerFactory(logger.GetLogger()),
	}

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
	webrtcSettings.SetIncludeLoopbackCandidate(p.Whip.EnableLoopbackCandidate)
	webrtcSettings.DisableSRTPReplayProtection(true)
	webrtcSettings.DisableSRTCPReplayProtection(true)

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

	sdpAnswer, err := s.getSDPAnswer(ctx, offer)
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
	s.trackLock.Lock()
	defer s.trackLock.Unlock()

	for _, v := range s.trackSrc {
		err := v.Start()
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *WHIPSource) Close() error {
	if s.pc == nil {
		return nil
	}

	s.pc.Close()

	return <-s.result
}

func (s *WHIPSource) GetSources(ctx context.Context) []*app.Source {
	for {
		s.trackLock.Lock()
		if len(s.trackSrc) >= s.expectedTrackCount {
			ret := make([]*app.Source, 0)

			for _, v := range s.trackSrc {
				ret = append(ret, v.GetAppSource())
			}

			s.trackLock.Unlock()
			return ret
		}
		s.trackLock.Unlock()

		select {
		case <-ctx.Done():
			logger.Infow("GetSources cancelled before all sources were ready")
			return nil
		case <-s.trackAddedChan:
			// continue
		}
	}
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

	closeOnce := sync.Once{}
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		logger.Infow("Peer Connection State changed", "state", state.String())

		// TODO support ICE Restart
		if state >= webrtc.PeerConnectionStateDisconnected {
			closeOnce.Do(func() {
				s.sync.End()

				var errs utils.ErrArray
				s.trackLock.Lock()
				for _, v := range s.trackSrc {
					err := v.Close()
					if err != nil {
						errs.AppendErr(err)
					}
				}
				s.trackLock.Unlock()

				s.result <- errs.ToError()
			})
		}
	})

	return pc, nil
}

func (s *WHIPSource) getSDPAnswer(ctx context.Context, offer *webrtc.SessionDescription) (string, error) {
	// Set the remote SessionDescription
	err := s.pc.SetRemoteDescription(*offer)
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
	kind := streamKindFromCodecType(track.Kind())
	logger.Infow("track has started", "type", track.PayloadType(), "codec", track.Codec().MimeType, "kind", kind)

	s.trackLock.Lock()
	defer s.trackLock.Unlock()
	s.tracks[track.ID()] = track
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

	select {
	case s.trackAddedChan <- track:
	default:
		logger.Warnw("failed notifying of new track", errors.New("channel full"))
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

func (w *WHIPSource) writePLI(ssrc webrtc.SSRC) {
	logger.Debugw("sending PLI request", "ssrc", ssrc)
	pli := []rtcp.Packet{
		&rtcp.PictureLossIndication{SenderSSRC: uint32(ssrc), MediaSSRC: uint32(ssrc)},
	}
	err := w.pc.WriteRTCP(pli)
	if err != nil {
		logger.Warnw("failed writing PLI", err, "ssrc", ssrc)
	}
}

func getExpectedTrackCount(offer *webrtc.SessionDescription) (int, error) {
	parsed, err := offer.Unmarshal()
	if err != nil {
		return 0, err
	}

	return len(parsed.MediaDescriptions), nil
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
