package media

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	lksdk "github.com/livekit/server-sdk-go"
)

type WebRTCSink struct {
	logger logger.Logger

	room *lksdk.Room

	audioOptions *livekit.IngressAudioOptions
	videoOptions *livekit.IngressVideoOptions
}

func NewWebRTCSink(ctx context.Context, conf *config.Config, p *Params) (*WebRTCSink, error) {
	ctx, span := tracer.Start(ctx, "media.NewWebRTCSink")
	defer span.End()

	lksdk.SetLogger(logr.Logger(p.Logger))
	room, err := lksdk.ConnectToRoom(
		p.WsUrl,
		lksdk.ConnectInfo{
			APIKey:              p.ApiKey,
			APISecret:           p.ApiSecret,
			RoomName:            p.Room,
			ParticipantName:     p.ParticipantName,
			ParticipantIdentity: p.ParticipantIdentity,
		},
		lksdk.NewRoomCallback(),
		lksdk.WithAutoSubscribe(false),
	)
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
			DisableDTX: s.audioOptions.DisableDtx,
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

	onRTCP := func(pkt rtcp.Packet) {
		switch pkt.(type) {
		case *rtcp.PictureLossIndication:
			s.logger.Debugw("PLI received")
			if err := encoder.ForceKeyFrame(); err != nil {
				s.logger.Errorw("could not force key frame", err)
			}
		}
	}
	track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{MimeType: mimeType}, lksdk.WithRTCPHandler(onRTCP))
	if err != nil {
		s.logger.Errorw("could not create track", err)
		return nil, err
	}

	var pub *lksdk.LocalTrackPublication
	onComplete := func() {
		s.logger.Debugw("write complete")
		if pub != nil {
			if err := s.room.LocalParticipant.UnpublishTrack(pub.SID()); err != nil {
				s.logger.Errorw("could not unpublish track", err)
			}
		}
	}
	track.OnBind(func() {
		if err := track.StartWrite(encoder, onComplete); err != nil {
			s.logger.Errorw("could not start writing", err)
		}
	})

	pub, err = s.room.LocalParticipant.PublishTrack(track, opts)
	if err != nil {
		s.logger.Errorw("could not publish track", err)
		return nil, err
	}

	return encoder.bin, nil
}

func (s *WebRTCSink) Close() {
	s.logger.Debugw("disconnecting from room")
	s.room.Disconnect()
}
