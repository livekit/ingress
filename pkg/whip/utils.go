package whip

import (
	"bytes"
	"strings"
	"time"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/media-sdk/jitter"
	"github.com/livekit/protocol/logger"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
)

func createDepacketizer(track *webrtc.TrackRemote) (rtp.Depacketizer, error) {
	var depacketizer rtp.Depacketizer

	switch strings.ToLower(track.Codec().MimeType) {
	case strings.ToLower(webrtc.MimeTypeVP8):
		depacketizer = &codecs.VP8Packet{}

	case strings.ToLower(webrtc.MimeTypeH264):
		depacketizer = &codecs.H264Packet{}

	case strings.ToLower(webrtc.MimeTypeOpus):
		depacketizer = &codecs.OpusPacket{}

	default:
		return nil, errors.ErrUnsupportedDecodeMimeType(track.Codec().MimeType)
	}

	return depacketizer, nil
}

func createJitterBuffer(
	track *webrtc.TrackRemote,
	logger logger.Logger,
	writePLI func(ssrc webrtc.SSRC),
	onPacket func([]jitter.ExtPacket),
) (*jitter.Buffer, error) {
	var maxLatency time.Duration
	options := []jitter.Option{jitter.WithLogger(logger)}

	depacketizer, err := createDepacketizer(track)
	if err != nil {
		return nil, err
	}

	switch strings.ToLower(track.Codec().MimeType) {
	case strings.ToLower(webrtc.MimeTypeH264), strings.ToLower(webrtc.MimeTypeVP8):
		maxLatency = maxVideoLatency
		options = append(options, jitter.WithPacketLossHandler(func(_, _ uint64) { writePLI(track.SSRC()) }))

	case strings.ToLower(webrtc.MimeTypeOpus):
		maxLatency = maxAudioLatency
		// No PLI for audio

	default:
		return nil, errors.ErrUnsupportedDecodeMimeType(track.Codec().MimeType)
	}

	jb := jitter.NewBuffer(depacketizer, maxLatency, onPacket, options...)

	return jb, nil
}

// sdpfragSessionHeader is the minimal session header the SDP parser needs
// before the lines of a trickle-ice-sdpfrag body.
const sdpfragSessionHeader = "v=0\r\no=- 0 0 IN IP4 0.0.0.0\r\ns=-\r\nt=0 0\r\n"

func extractICEDetails(in []byte) (ufrag string, pwd string, err error) {
	scanAttributes := func(attributes []sdp.Attribute) {
		for _, a := range attributes {
			switch a.Key {
			case "ice-ufrag":
				ufrag = a.Value
			case "ice-pwd":
				pwd = a.Value
			}
		}
	}

	// A trickle-ice-sdpfrag body (RFC 8840) has no session header, which the
	// SDP parser requires. Some clients send a complete session description.
	if !bytes.HasPrefix(in, []byte("v=")) {
		in = append([]byte(sdpfragSessionHeader), in...)
	}

	var parsed sdp.SessionDescription
	if err = parsed.Unmarshal(in); err != nil {
		return
	}

	scanAttributes(parsed.Attributes)
	for _, m := range parsed.MediaDescriptions {
		scanAttributes(m.Attributes)
	}

	return
}
