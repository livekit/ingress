package media

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"

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

	ingressId string
}

func NewWebRTCSink(ctx context.Context, p *Params) (*WebRTCSink, error) {
	ctx, span := tracer.Start(ctx, "media.NewWebRTCSink")
	defer span.End()

	lksdk.SetLogger(logr.Logger(p.Logger))
	room, err := lksdk.ConnectToRoomWithToken(
		p.WsUrl,
		p.Token,
		lksdk.NewRoomCallback(),
		lksdk.WithAutoSubscribe(false),
	)
	if err != nil {
		return nil, err
	}

	p.SetRoomId(room.SID())

	return &WebRTCSink{
		room:         room,
		logger:       p.Logger,
		audioOptions: p.Audio,
		videoOptions: p.Video,
		ingressId:    p.IngressId,
	}, nil
}

func (s *WebRTCSink) AddTrackLayer(mimeType string, opts *lksdk.TrackPublicationOptions, trackOpts []lksdk.LocalSampleTrackOptions, output *Output) error {
	track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{MimeType: mimeType}, trackOpts...)
	if err != nil {
		s.logger.Errorw("could not create track", err)
		return err
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
		if err := track.StartWrite(output, onComplete); err != nil {
			s.logger.Errorw("could not start writing", err)
		}
	})

	pub, err = s.room.LocalParticipant.PublishTrack(track, opts)
	if err != nil {
		s.logger.Errorw("could not publish track", err)
		return err
	}

	return nil
}

// This may return more than 1 bin if simulcast is enabled
func (s *WebRTCSink) AddTrack(kind StreamKind) ([]*gst.Bin, error) {
	var mimeType string
	var output *Output
	var opts *lksdk.TrackPublicationOptions
	var trackOpts []lksdk.LocalSampleTrackOptions
	var err error

	onRTCP := func(pkt rtcp.Packet) {
		switch pkt.(type) {
		case *rtcp.PictureLossIndication:
			s.logger.Debugw("PLI received")
			if err := output.ForceKeyFrame(); err != nil {
				s.logger.Errorw("could not force key frame", err)
			}
		}
	}

	bins := make([]*gst.Bin, 0)
	switch kind {
	case Audio:
		mimeType = s.audioOptions.MimeType
		output, err = NewAudioOutput(s.audioOptions)
		opts = &lksdk.TrackPublicationOptions{
			Name:       s.audioOptions.Name,
			Source:     s.audioOptions.Source,
			DisableDTX: s.audioOptions.DisableDtx,
		}
		trackOpts = []lksdk.LocalSampleTrackOptions{
			lksdk.WithRTCPHandler(onRTCP),
		}

		if err != nil {
			s.logger.Errorw("could not create output", err)
			return nil, err
		}

		err = s.AddTrackLayer(mimeType, opts, trackOpts, output)
		if err != nil {
			s.logger.Errorw("could not add track", err)
			return nil, err
		}

		bins = append(bins, output.bin)

	case Video:
		mimeType = s.videoOptions.MimeType
		for _, layer := range s.videoOptions.Layers {
			output, err = NewVideoOutput(mimeType, layer)
			opts = &lksdk.TrackPublicationOptions{
				Name:        s.videoOptions.Name,
				Source:      s.videoOptions.Source,
				VideoWidth:  int(layer.Width),
				VideoHeight: int(layer.Height),
			}
			trackOpts = []lksdk.LocalSampleTrackOptions{
				lksdk.WithRTCPHandler(onRTCP),
				lksdk.WithSimulcast(s.ingressId, layer),
			}

			err = s.AddTrackLayer(mimeType, opts, trackOpts, output)
			if err != nil {
				s.logger.Errorw("could not add layer", err)
				return nil, err
			}

			bins = append(bins, output.bin)
		}
	}

	return bins, nil
}

func (s *WebRTCSink) Close() {
	s.logger.Debugw("disconnecting from room")
	s.room.Disconnect()
}
