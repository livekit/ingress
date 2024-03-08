package whip

import (
	"strings"
	"time"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/server-sdk-go/v2/pkg/jitter"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
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

func createJitterBuffer(track *webrtc.TrackRemote, logger logger.Logger, writePLI func(ssrc webrtc.SSRC)) (*jitter.Buffer, error) {
	var maxLatency time.Duration
	options := []jitter.Option{jitter.WithLogger(logger)}

	depacketizer, err := createDepacketizer(track)
	if err != nil {
		return nil, err
	}

	switch strings.ToLower(track.Codec().MimeType) {
	case strings.ToLower(webrtc.MimeTypeVP8):
		maxLatency = maxVideoLatency
		options = append(options, jitter.WithPacketDroppedHandler(func() { writePLI(track.SSRC()) }))

	case strings.ToLower(webrtc.MimeTypeH264):
		maxLatency = maxVideoLatency
		options = append(options, jitter.WithPacketDroppedHandler(func() { writePLI(track.SSRC()) }))

	case strings.ToLower(webrtc.MimeTypeOpus):
		maxLatency = maxAudioLatency
		// No PLI for audio

	default:
		return nil, errors.ErrUnsupportedDecodeMimeType(track.Codec().MimeType)
	}

	clockRate := track.Codec().ClockRate

	jb := jitter.NewBuffer(depacketizer, clockRate, maxLatency, options...)

	return jb, nil
}
