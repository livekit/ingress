package media

import (
	"context"
	"sync/atomic"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	"github.com/livekit/protocol/utils"
	lksdk "github.com/livekit/server-sdk-go"
)

type WebRTCSink struct {
	room *lksdk.Room

	params *params.Params
}

func NewWebRTCSink(ctx context.Context, p *params.Params) (*WebRTCSink, error) {
	ctx, span := tracer.Start(ctx, "media.NewWebRTCSink")
	defer span.End()

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
		room:   room,
		params: p,
	}, nil
}

func (s *WebRTCSink) addAudioTrack() (*Output, error) {
	output, err := NewAudioOutput(s.params.AudioEncodingOptions)
	opts := &lksdk.TrackPublicationOptions{
		Name:       s.params.Audio.Name,
		Source:     s.params.Video.Source,
		DisableDTX: s.params.AudioEncodingOptions.DisableDtx,
		Stereo:     s.params.AudioEncodingOptions.Channels > 1,
	}

	if err != nil {
		logger.Errorw("could not create output", err)
		return nil, err
	}

	track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{MimeType: utils.GetMimeTypeForAudioCodec(s.params.AudioEncodingOptions.AudioCodec)})
	if err != nil {
		logger.Errorw("could not create audio track", err)
		return nil, err
	}

	var pub *lksdk.LocalTrackPublication
	onComplete := func() {
		logger.Debugw("audio track write complete, unpublishing audio track")
		if pub != nil {
			if err := s.room.LocalParticipant.UnpublishTrack(pub.SID()); err != nil {
				logger.Errorw("could not unpublish audio track", err)
			}
		}
	}
	track.OnBind(func() {
		if err := track.StartWrite(output, onComplete); err != nil {
			logger.Errorw("could not start writing audio track", err)
		}
	})

	pub, err = s.room.LocalParticipant.PublishTrack(track, opts)
	if err != nil {
		logger.Errorw("could not publish audio track", err)
		return nil, err
	}

	return output.Output, nil
}

func (s *WebRTCSink) addVideoTrack() ([]*Output, error) {
	opts := &lksdk.TrackPublicationOptions{
		Name:        s.params.Video.Name,
		Source:      s.params.Video.Source,
		VideoWidth:  int(s.params.VideoEncodingOptions.Layers[0].Width),
		VideoHeight: int(s.params.VideoEncodingOptions.Layers[0].Height),
	}

	var pub *lksdk.LocalTrackPublication
	var err error
	var activeLayerCount int32
	onComplete := func() {
		logger.Debugw("video track layer write complete")
		if pub != nil {
			if atomic.AddInt32(&activeLayerCount, -1) == 0 {
				logger.Debugw("unpublishing video track")
				if err := s.room.LocalParticipant.UnpublishTrack(pub.SID()); err != nil {
					logger.Errorw("could not unpublish video track", err)
				}
			}
		}
	}

	outputs := make([]*Output, 0)
	tracks := make([]*lksdk.LocalSampleTrack, 0)
	for _, layer := range s.params.VideoEncodingOptions.Layers {
		output, err := NewVideoOutput(s.params.VideoEncodingOptions.VideoCodec, layer)

		onRTCP := func(pkt rtcp.Packet) {
			switch pkt.(type) {
			case *rtcp.PictureLossIndication:
				logger.Debugw("PLI received")
				if err := output.ForceKeyFrame(); err != nil {
					logger.Errorw("could not force key frame", err)
				}
			}
		}
		track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{
			MimeType: utils.GetMimeTypeForVideoCodec(s.params.VideoEncodingOptions.VideoCodec),
		},
			lksdk.WithRTCPHandler(onRTCP), lksdk.WithSimulcast(s.params.IngressId, layer))
		if err != nil {
			logger.Errorw("could not create video track", err)
			return nil, err
		}

		track.OnBind(func() {
			if err := track.StartWrite(output, onComplete); err != nil {
				logger.Errorw("could not start writing video track", err)
			}
		})
		tracks = append(tracks, track)
		outputs = append(outputs, output.Output)
	}

	pub, err = s.room.LocalParticipant.PublishSimulcastTrack(tracks, opts)
	if err != nil {
		logger.Errorw("could not publish video track", err)
		return nil, err
	}
	activeLayerCount = int32(len(tracks))

	logger.Debugw("published video track")

	return outputs, nil
}

func (s *WebRTCSink) AddTrack(kind StreamKind) (*gst.Bin, error) {
	var bin *gst.Bin

	switch kind {
	case Audio:
		output, err := s.addAudioTrack()
		if err != nil {
			logger.Errorw("could not add audio track", err)
			return nil, err
		}

		bin = output.bin

	case Video:
		outputs, err := s.addVideoTrack()
		if err != nil {
			logger.Errorw("could not add video track", err)
			return nil, err
		}

		pp, err := NewVideoOutputBin(s.params.VideoEncodingOptions, outputs)
		if err != nil {
			logger.Errorw("could not create tee", err)
			return nil, err
		}

		bin = pp.GetBin()
	}

	return bin, nil
}

func (s *WebRTCSink) Close() {
	logger.Debugw("disconnecting from room")
	s.room.Disconnect()
}
