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
	"sync/atomic"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"

	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/mediatransportutil/pkg/pacer"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

type SampleProvider interface {
	Close() error
}

type KeyFrameEmitter interface {
	ForceKeyFrame() error
}

type PLIHandler struct {
	p atomic.Pointer[KeyFrameEmitter]
}

func (h *PLIHandler) HandlePLI() error {
	p := h.p.Load()

	if p != nil {
		return (*p).ForceKeyFrame()
	}

	return nil
}

func (h *PLIHandler) SetKeyFrameEmitter(p KeyFrameEmitter) {
	h.p.Store(&p)
}

type LKSDKOutput struct {
	logger logger.Logger
	room   *lksdk.Room
	params *params.Params

	lock    sync.Mutex
	outputs []SampleProvider
}

func NewLKSDKOutput(ctx context.Context, p *params.Params) (*LKSDKOutput, error) {
	ctx, span := tracer.Start(ctx, "lksdk.NewLKSDKOutput")
	defer span.End()

	s := &LKSDKOutput{
		params: p,
	}

	cb := lksdk.NewRoomCallback()
	cb.OnDisconnected = func() {
		s.Close()
	}

	opts := []lksdk.ConnectOption{
		lksdk.WithAutoSubscribe(false),
	}

	if !p.BypassTranscoding {
		var br uint32
		if p.VideoEncodingOptions != nil {
			for _, l := range p.VideoEncodingOptions.Layers {
				br += l.Bitrate
			}
		}
		if p.AudioEncodingOptions != nil {
			br += p.AudioEncodingOptions.Bitrate
		}

		if br > 0 {
			// Use 2x the nominal bitrate
			br *= 2

			pf := pacer.NewPacerFactory(
				pacer.LeakyBucketPacer,
				pacer.WithBitrate(int(br)),
				pacer.WithMaxLatency(time.Second),
			)

			opts = append(opts, lksdk.WithPacer(pf))

			p.GetLogger().Infow("enabling pacer", "bitrate", br)
		}
	}

	room, err := lksdk.ConnectToRoomWithToken(
		p.WsUrl,
		p.Token,
		cb,
		opts...,
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

func (s *LKSDKOutput) AddAudioTrack(mimeType string, disableDTX bool, stereo bool) (*lksdk.LocalTrack, error) {
	opts := &lksdk.TrackPublicationOptions{
		Name:       s.params.Audio.Name,
		Source:     s.params.Audio.Source,
		DisableDTX: disableDTX,
		Stereo:     stereo,
	}

	track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{MimeType: mimeType})
	if err != nil {
		s.logger.Errorw("could not create audio track", err)
		return nil, err
	}

	track.OnBind(func() {
		s.logger.Debugw("audio track bound")
	})

	track.OnUnbind(func() {
		s.logger.Debugw("audio track unbound")
	})

	_, err = s.room.LocalParticipant.PublishTrack(track, opts)
	if err != nil {
		s.logger.Errorw("could not publish audio track", err)
		return nil, err
	}

	return track, nil
}

func (s *LKSDKOutput) AddVideoTrack(layers []*livekit.VideoLayer, mimeType string) ([]*lksdk.LocalTrack, []*PLIHandler, error) {
	opts := &lksdk.TrackPublicationOptions{
		Name:        s.params.Video.Name,
		Source:      s.params.Video.Source,
		VideoWidth:  int(layers[0].Width),
		VideoHeight: int(layers[0].Height),
	}

	var err error

	tracks := make([]*lksdk.LocalSampleTrack, 0)
	pliHandlers := make([]*PLIHandler, 0)
	for _, layer := range layers {
		pliHandler := &PLIHandler{}
		pliHandlers = append(pliHandlers, pliHandler)

		onRTCP := func(pkt rtcp.Packet) {
			switch pkt.(type) {
			case *rtcp.PictureLossIndication:
				if err := pliHandler.HandlePLI(); err != nil {
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
			return nil, nil, err
		}

		localLayer := layer
		track.OnBind(func() {
			s.logger.Debugw("video track bound", "layer", localLayer.Quality.String())
		})
		track.OnUnbind(func() {
			s.logger.Debugw("video track unbound", "layer", localLayer.Quality.String())
		})

		tracks = append(tracks, track)
	}

	_, err = s.room.LocalParticipant.PublishSimulcastTrack(tracks, opts)
	if err != nil {
		s.logger.Errorw("could not publish video track", err)
		return nil, nil, err
	}

	s.logger.Debugw("published video track")

	return tracks, pliHandlers, nil
}

func (s *LKSDKOutput) AddOutputs(o ...SampleProvider) {
	s.lock.Lock()
	s.outputs = append(s.outputs, o...)
	s.lock.Unlock()
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
