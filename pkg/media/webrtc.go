package media

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/ingress/pkg/errors"
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

func (s *WebRTCSink) addAudioTrack() (*Output, error) {
	output, err := NewAudioOutput(s.audioOptions)
	opts := &lksdk.TrackPublicationOptions{
		Name:       s.audioOptions.Name,
		Source:     s.audioOptions.Source,
		DisableDTX: s.audioOptions.DisableDtx,
	}

	if err != nil {
		s.logger.Errorw("could not create output", err)
		return nil, err
	}

	track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{MimeType: s.audioOptions.MimeType})
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
		if err := track.StartWrite(output, onComplete); err != nil {
			s.logger.Errorw("could not start writing", err)
		}
	})

	pub, err = s.room.LocalParticipant.PublishTrack(track, opts)
	if err != nil {
		s.logger.Errorw("could not publish track", err)
		return nil, err
	}

	return output, nil
}

func (s *WebRTCSink) addVideoTrack() ([]*Output, error) {
	opts := &lksdk.TrackPublicationOptions{
		Name:        s.videoOptions.Name,
		Source:      s.videoOptions.Source,
		VideoWidth:  int(s.videoOptions.Layers[0].Width),
		VideoHeight: int(s.videoOptions.Layers[0].Height),
	}

	var pub *lksdk.LocalTrackPublication
	var err error
	onComplete := func() {
		s.logger.Debugw("write complete")
		if pub != nil {
			if err := s.room.LocalParticipant.UnpublishTrack(pub.SID()); err != nil {
				s.logger.Errorw("could not unpublish track", err)
			}
		}
	}

	outputs := make([]*Output, 0)
	tracks := make([]*lksdk.LocalSampleTrack, 0)
	for _, layer := range s.videoOptions.Layers {
		output, err := NewVideoOutput(s.videoOptions.MimeType, layer)

		onRTCP := func(pkt rtcp.Packet) {
			switch pkt.(type) {
			case *rtcp.PictureLossIndication:
				s.logger.Debugw("PLI received")
				if err := output.ForceKeyFrame(); err != nil {
					s.logger.Errorw("could not force key frame", err)
				}
			}
		}
		track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{MimeType: s.videoOptions.MimeType}, lksdk.WithRTCPHandler(onRTCP), lksdk.WithSimulcast(s.ingressId, layer))
		if err != nil {
			s.logger.Errorw("could not create track", err)
			return nil, err
		}

		track.OnBind(func() {
			if err := track.StartWrite(output, onComplete); err != nil {
				s.logger.Errorw("could not start writing", err)
			}
		})
		tracks = append(tracks, track)
		outputs = append(outputs, output)
	}

	pub, err = s.room.LocalParticipant.PublishSimulcastTrack(tracks, opts)
	if err != nil {
		s.logger.Errorw("could not publish track", err)
		return nil, err
	}

	return outputs, nil
}

func (s *WebRTCSink) createTee(outputs []*Output) (*gst.Bin, error) {
	tee, err := gst.NewElement("tee")
	if err != nil {
		return nil, err
	}

	bin := gst.NewBin("tee")
	err = bin.Add(tee)
	if err != nil {
		return nil, err
	}

	for _, output := range outputs {
		err := bin.Add(output.bin.Element)
		if err != nil {
			return nil, err
		}

		err = gst.ElementLinkMany(tee, output.bin.Element)
		if err != nil {
			return nil, err
		}
	}

	binSink := gst.NewGhostPad("sink", tee.GetStaticPad("sink"))
	if !bin.AddPad(binSink.Pad) {
		return nil, errors.ErrUnableToAddPad
	}

	return bin, nil
}

func (s *WebRTCSink) AddTrack(kind StreamKind) (*gst.Bin, error) {
	var bin *gst.Bin

	switch kind {
	case Audio:
		output, err := s.addAudioTrack()
		if err != nil {
			s.logger.Errorw("could not add audio track", err)
			return nil, err
		}

		bin = output.bin

	case Video:
		outputs, err := s.addVideoTrack()
		if err != nil {
			s.logger.Errorw("could not add video track", err)
			return nil, err
		}

		bin, err = s.createTee(outputs)
		if err != nil {
			s.logger.Errorw("could not create tee", err)
			return nil, err
		}
	}

	return bin, nil
}

func (s *WebRTCSink) Close() {
	s.logger.Debugw("disconnecting from room")
	s.room.Disconnect()
}
