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
	"sync"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"

	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	lksdk "github.com/livekit/server-sdk-go"
)

type RTPProvider interface {
	Close() error
	ForceKeyFrame() error
}

type LKSDKOutput struct {
	logger logger.Logger
	room   *lksdk.Room
	params *params.Params

	lock    sync.Mutex
	outputs []RTPProvider
}

func NewLKSDKOutput(ctx context.Context, p *params.Params) (*LKSDKOutput, error) {
	_, span := tracer.Start(ctx, "lksdk.NewLKSDKOutput")
	defer span.End()

	s := &LKSDKOutput{
		params: p,
	}

	cb := lksdk.NewRoomCallback()
	cb.OnDisconnected = func() {
		s.Close()
	}

	room, err := lksdk.ConnectToRoomWithToken(
		p.WsUrl,
		p.Token,
		cb,
		lksdk.WithAutoSubscribe(false),
	)
	if err != nil {
		return nil, err
	}

	s.room = room
	s.logger = p.GetLogger().WithValues("roomID", room.SID())

	s.logger.Infow("connected to room")

	p.SetRoomId(room.SID())

	return s, nil
}

func (s *LKSDKOutput) AddAudioTrack(output RTPProvider, mimeType string, disableDTX bool, stereo bool) (*lksdk.LocalTrack, error) {
	opts := &lksdk.TrackPublicationOptions{
		Name:       s.params.Audio.Name,
		Source:     s.params.Audio.Source,
		DisableDTX: disableDTX,
		Stereo:     stereo,
	}

	track, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{MimeType: mimeType})
	if err != nil {
		s.logger.Errorw("could not create audio track", err)
		return nil, err
	}

	track.OnBind(func() {
		s.logger.Debugw("audio track start write")
	})

	if _, err = s.room.LocalParticipant.PublishTrack(track, opts); err != nil {
		s.logger.Errorw("could not publish audio track", err)
		return nil, err
	}

	s.lock.Lock()
	s.outputs = append(s.outputs, output)
	s.lock.Unlock()

	return track, nil
}

func (s *LKSDKOutput) AddVideoTrack(outputs []RTPProvider, layers []*livekit.VideoLayer, mimeType string) ([]*lksdk.LocalTrack, error) {
	opts := &lksdk.TrackPublicationOptions{
		Name:        s.params.Video.Name,
		Source:      s.params.Video.Source,
		VideoWidth:  int(layers[0].Width),
		VideoHeight: int(layers[0].Height),
	}

	tracks := make([]*lksdk.LocalTrack, 0)
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

		track, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{
			MimeType: mimeType,
		},
			lksdk.WithRTCPHandler(onRTCP), lksdk.WithSimulcast(s.params.IngressId, layer))
		if err != nil {
			s.logger.Errorw("could not create video track", err)
			return nil, err
		}

		localLayer := layer
		track.OnBind(func() {
			s.logger.Debugw("video track start write", "layer", localLayer.Quality.String())
		})

		tracks = append(tracks, track)

		s.lock.Lock()
		s.outputs = append(s.outputs, output)
		s.lock.Unlock()
	}

	if _, err := s.room.LocalParticipant.PublishSimulcastTrack(tracks, opts); err != nil {
		s.logger.Errorw("could not publish video track", err)
		return nil, err
	}

	s.logger.Debugw("published video track")

	return tracks, nil
}

func (s *LKSDKOutput) Close() {
	s.logger.Debugw("disconnecting from room")

	s.lock.Lock()
	defer s.lock.Unlock()

	for _, o := range s.outputs {
		o.Close()
	}
	// only close the outputs once
	s.outputs = nil

	s.room.Disconnect()
}
