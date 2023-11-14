// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package lksdk_output

import (
	"context"
	"sync/atomic"

	"github.com/frostbyte73/core"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"

	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	lksdk "github.com/livekit/server-sdk-go"
)

type VideoSampleProvider interface {
	lksdk.SampleProvider

	ForceKeyFrame() error
}

type LKSDKOutput struct {
	logger logger.Logger
	room   *lksdk.Room
	closed core.Fuse

	params *params.Params
}

func NewLKSDKOutput(ctx context.Context, p *params.Params) (*LKSDKOutput, error) {
	ctx, span := tracer.Start(ctx, "lksdk.NewLKSDKOutput")
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

	s := &LKSDKOutput{
		room:   room,
		params: p,
		logger: logger.GetLogger().WithValues("ingressID", p.IngressId, "resourceID", p.State.ResourceId, "roomID", room.SID()),
		closed: core.NewFuse(),
	}

	s.logger.Infow("connected to room")

	p.SetRoomId(room.SID())

	return s, nil
}

func (s *LKSDKOutput) AddAudioTrack(output lksdk.SampleProvider, mimeType string, disableDTX bool, stereo bool) error {
	opts := &lksdk.TrackPublicationOptions{
		Name:       s.params.Audio.Name,
		Source:     s.params.Audio.Source,
		DisableDTX: disableDTX,
		Stereo:     stereo,
	}

	track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{MimeType: mimeType})
	if err != nil {
		s.logger.Errorw("could not create audio track", err)
		return err
	}

	var pub *lksdk.LocalTrackPublication
	onComplete := func() {
		s.logger.Debugw("audio track write complete, unpublishing audio track")
		// don't unpublish if the completion is due to the output closing
		if pub != nil && !s.closed.IsBroken() {
			if err := s.room.LocalParticipant.UnpublishTrack(pub.SID()); err != nil {
				s.logger.Errorw("could not unpublish audio track", err)
			}
		}
		output.Close()
	}
	track.OnBind(func() {
		// Start write is idempotent if the sample provider doesn't change
		if err := track.StartWrite(output, onComplete); err != nil {
			s.logger.Errorw("could not start writing audio track", err)
		}
	})

	pub, err = s.room.LocalParticipant.PublishTrack(track, opts)
	if err != nil {
		s.logger.Errorw("could not publish audio track", err)
		return err
	}

	return nil
}

func (s *LKSDKOutput) AddVideoTrack(outputs []VideoSampleProvider, layers []*livekit.VideoLayer, mimeType string) error {
	opts := &lksdk.TrackPublicationOptions{
		Name:        s.params.Video.Name,
		Source:      s.params.Video.Source,
		VideoWidth:  int(layers[0].Width),
		VideoHeight: int(layers[0].Height),
	}

	var pub *lksdk.LocalTrackPublication
	var err error
	var activeLayerCount int32

	getOnComplete := func(layer *livekit.VideoLayer, output VideoSampleProvider) func() {
		return func() {
			s.logger.Debugw("video track layer write complete", "layer", layer.Quality.String())
			// don't unpublish if the completion is due to the output closing
			if pub != nil && !s.closed.IsBroken() {
				if atomic.AddInt32(&activeLayerCount, -1) == 0 {
					s.logger.Debugw("unpublishing video track")
					if err := s.room.LocalParticipant.UnpublishTrack(pub.SID()); err != nil {
						s.logger.Errorw("could not unpublish video track", err)
					}
				}
			}
			output.Close()
		}
	}

	tracks := make([]*lksdk.LocalSampleTrack, 0)
	for i, layer := range layers {
		output := outputs[i]

		onRTCP := func(pkt rtcp.Packet) {
			switch pkt.(type) {
			case *rtcp.PictureLossIndication:
				s.logger.Debugw("PLI received")
				if err := output.ForceKeyFrame(); err != nil {
					s.logger.Errorw("could not force key frame", err)
				}
			}
		}
		track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{
			MimeType: mimeType,
		},
			lksdk.WithRTCPHandler(onRTCP), lksdk.WithSimulcast(s.params.IngressId, layer))
		if err != nil {
			s.logger.Errorw("could not create video track", err)
			return err
		}

		onComplete := getOnComplete(layer, output)
		track.OnBind(func() {
			if err := track.StartWrite(output, onComplete); err != nil {
				s.logger.Errorw("could not start writing video track", err)
			}
		})
		tracks = append(tracks, track)
	}

	pub, err = s.room.LocalParticipant.PublishSimulcastTrack(tracks, opts)
	if err != nil {
		s.logger.Errorw("could not publish video track", err)
		return err
	}
	activeLayerCount = int32(len(tracks))

	s.logger.Debugw("published video track")

	return nil
}

func (s *LKSDKOutput) Close() {
	s.logger.Debugw("disconnecting from room")

	s.closed.Break()

	s.room.Disconnect()
}
