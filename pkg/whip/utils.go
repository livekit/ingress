package whip

import (
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
	case strings.ToLower(webrtc.MimeTypeVP8):
		maxLatency = maxVideoLatency
		options = append(options, jitter.WithPacketLossHandler(func() { writePLI(track.SSRC()) }))

	case strings.ToLower(webrtc.MimeTypeH264):
		maxLatency = maxVideoLatency
		options = append(options, jitter.WithPacketLossHandler(func() { writePLI(track.SSRC()) }))

	case strings.ToLower(webrtc.MimeTypeOpus):
		maxLatency = maxAudioLatency
		// No PLI for audio

	default:
		return nil, errors.ErrUnsupportedDecodeMimeType(track.Codec().MimeType)
	}

	jb := jitter.NewBuffer(depacketizer, maxLatency, onPacket, options...)

	return jb, nil
}

func extractICEDetails(in []byte) (ufrag string, pwd string, err error) {
	scanAttributes := func(attributes []sdp.Attribute) {
		for _, a := range attributes {
			if a.Key == "ice-ufrag" {
				ufrag = a.Value
			} else if a.Key == "ice-pwd" {
				pwd = a.Value
			}
		}
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

func replaceICEDetails(in, ufrag, pwd string) (string, error) {
	var parsed sdp.SessionDescription
	replaceAttributes := func(attributes []sdp.Attribute) {
		for i := range attributes {
			if attributes[i].Key == "ice-ufrag" {
				attributes[i].Value = ufrag
			} else if attributes[i].Key == "ice-pwd" {
				attributes[i].Value = pwd
			}
		}
	}

	if err := parsed.UnmarshalString(in); err != nil {
		return "", err
	}

	replaceAttributes(parsed.Attributes)
	for _, m := range parsed.MediaDescriptions {
		replaceAttributes(m.Attributes)
	}

	newRemoteDescription, err := parsed.Marshal()
	if err != nil {
		return "", err
	}

	return string(newRemoteDescription), nil
}
