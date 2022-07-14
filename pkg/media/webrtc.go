package media

import (
	"context"

	"github.com/pion/rtcp"
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	lksdk "github.com/livekit/server-sdk-go"
)

type WebRTCSink struct {
	logger logger.Logger

	room     *lksdk.Room
	audioPub *lksdk.LocalTrackPublication
	videoPub *lksdk.LocalTrackPublication

	audioOptions *livekit.IngressAudioOptions
	videoOptions *livekit.IngressVideoOptions
}

func NewWebRTCSink(ctx context.Context, conf *config.Config, p *Params) (*WebRTCSink, error) {
	ctx, span := tracer.Start(ctx, "media.NewWebRTCSink")
	defer span.End()

	callbacks := &lksdk.RoomCallback{}

	room, err := lksdk.ConnectToRoom(p.WsUrl, lksdk.ConnectInfo{
		APIKey:              p.ApiKey,
		APISecret:           p.ApiSecret,
		RoomName:            p.Room,
		ParticipantName:     p.ParticipantName,
		ParticipantIdentity: p.ParticipantIdentity,
	}, callbacks)
	if err != nil {
		return nil, err
	}

	return &WebRTCSink{
		room:         room,
		logger:       p.Logger,
		audioOptions: p.AudioOptions,
		videoOptions: p.VideoOptions,
	}, nil
}

func (s *WebRTCSink) AddTrack(kind StreamKind) (*gst.Bin, error) {
	var mimeType string
	var encoder *Encoder
	var opts *lksdk.TrackPublicationOptions
	var err error

	switch kind {
	case Audio:
		mimeType = s.audioOptions.MimeType
		encoder, err = NewAudioEncoder(s.audioOptions)
		opts = &lksdk.TrackPublicationOptions{
			Name:       s.audioOptions.Name,
			Source:     s.audioOptions.Source,
			DisableDTX: !s.audioOptions.Dtx, // TODO: change to DisableDtx
		}

	case Video:
		mimeType = s.videoOptions.MimeType
		layer := s.videoOptions.Layers[0]
		encoder, err = NewVideoEncoder(mimeType, layer)
		opts = &lksdk.TrackPublicationOptions{
			Name:        s.videoOptions.Name,
			Source:      s.videoOptions.Source,
			VideoWidth:  int(layer.Width),
			VideoHeight: int(layer.Height),
		}
	}

	if err != nil {
		s.logger.Errorw("could not create encoder", err)
		return nil, err
	}

	track, err := lksdk.NewLocalReaderTrack(encoder, mimeType, lksdk.ReaderTrackWithRTCPHandler(
		func(pkt rtcp.Packet) {
			switch pkt.(type) {
			case *rtcp.PictureLossIndication:
				s.logger.Debugw("PLI received")
				if err := encoder.ForceKeyFrame(); err != nil {
					s.logger.Errorw("could not force key frame", err)
				}
			}
		},
	))
	if err != nil {
		s.logger.Errorw("could not create track", err)
		return nil, err
	}

	pub, err := s.room.LocalParticipant.PublishTrack(track, opts)
	if err != nil {
		s.logger.Errorw("could not publish track", err)
		return nil, err
	}

	switch kind {
	case Audio:
		s.audioPub = pub
	case Video:
		s.videoPub = pub
	}

	return encoder.bin, nil
}
