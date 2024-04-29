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

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/mediatransportutil/pkg/pacer"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

const (
	watchdogDeadline = time.Minute
)

type SampleProvider interface {
	Close() error
}
type KeyFrameEmitter interface {
	ForceKeyFrame() error
}

type PacketSink interface {
	HandleRTCPPacket(pkt rtcp.Packet) error
}

type RTCPHandler struct {
	p atomic.Pointer[PacketSink]
	k atomic.Pointer[KeyFrameEmitter]
}

func (h *RTCPHandler) HandleRTCP(pkt rtcp.Packet) error {
	p := h.p.Load()

	if p != nil {
		return (*p).HandleRTCPPacket(pkt)
	}

	return nil
}

func (h *RTCPHandler) SetPacketSink(p PacketSink) {
	h.p.Store(&p)
}

func (h *RTCPHandler) HandlePLI() error {
	k := h.k.Load()

	if k != nil {
		return (*k).ForceKeyFrame()
	}

	return nil
}

func (h *RTCPHandler) SetKeyFrameEmitter(k KeyFrameEmitter) {
	h.k.Store(&k)
}

type LKSDKOutput struct {
	logger logger.Logger
	room   *lksdk.Room
	params *params.Params

	errChan  chan error
	watchdog *Watchdog

	lock    sync.Mutex
	outputs []SampleProvider
}

func NewLKSDKOutput(ctx context.Context, p *params.Params) (*LKSDKOutput, error) {
	ctx, span := tracer.Start(ctx, "lksdk.NewLKSDKOutput")
	defer span.End()

	s := &LKSDKOutput{
		params:  p,
		errChan: make(chan error, 1),
		logger:  p.GetLogger(),
	}

	s.watchdog = NewWatchdog(func() {
		s.logger.Warnw("disconnection from room triggered by watchdog", errors.ErrRoomDisconnectedUnexpectedly)

		select {
		case s.errChan <- errors.ErrRoomDisconnectedUnexpectedly:
		default:
		}

		s.closeOutput()
	}, watchdogDeadline)

	cb := lksdk.NewRoomCallback()
	cb.OnDisconnectedWithReason = func(reason lksdk.DisconnectionReason) {
		var err error
		switch reason {
		case lksdk.Failed:
			err = errors.ErrRoomDisconnectedUnexpectedly
		default:
			err = errors.ErrRoomDisconnected
		}

		// Only store first error
		select {
		case s.errChan <- err:
		default:
		}

		s.closeOutput()
	}

	opts := []lksdk.ConnectOption{
		lksdk.WithAutoSubscribe(false),
	}

	if !*p.EnableTranscoding {
		opts = append(opts, lksdk.WithInterceptors([]interceptor.Factory{}))
	} else {
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
		s.watchdog.TrackBound()
		s.logger.Debugw("audio track bound")
	})

	track.OnUnbind(func() {
		s.watchdog.TrackUnbound()
		s.logger.Debugw("audio track unbound")
	})

	_, err = s.room.LocalParticipant.PublishTrack(track, opts)
	if err != nil {
		s.logger.Errorw("could not publish audio track", err)
		return nil, err
	}

	s.watchdog.TrackAdded()

	return track, nil
}

func (s *LKSDKOutput) AddVideoTrack(layers []*livekit.VideoLayer, mimeType string) ([]*lksdk.LocalTrack, []*RTCPHandler, error) {
	opts := &lksdk.TrackPublicationOptions{
		Name:        s.params.Video.Name,
		Source:      s.params.Video.Source,
		VideoWidth:  int(layers[0].Width),
		VideoHeight: int(layers[0].Height),
	}

	var err error

	tracks := make([]*lksdk.LocalSampleTrack, 0)
	rtcpHandlers := make([]*RTCPHandler, 0)
	for _, layer := range layers {
		rtcpHandler := &RTCPHandler{}
		rtcpHandlers = append(rtcpHandlers, rtcpHandler)

		onRTCP := func(pkt rtcp.Packet) {
			switch pkt.(type) {
			case *rtcp.PictureLossIndication:
				if err := rtcpHandler.HandlePLI(); err != nil {
					s.logger.Errorw("could not force key frame", err)
				}
			}

			if err := rtcpHandler.HandleRTCP(pkt); err != nil {
				s.logger.Errorw("RTCP message handling failed", err)
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
			s.watchdog.TrackBound()
			s.logger.Debugw("video track bound", "layer", localLayer.Quality.String())
		})
		track.OnUnbind(func() {
			s.watchdog.TrackUnbound()
			s.logger.Debugw("video track unbound", "layer", localLayer.Quality.String())
		})

		tracks = append(tracks, track)

		s.watchdog.TrackAdded()
	}

	_, err = s.room.LocalParticipant.PublishSimulcastTrack(tracks, opts)
	if err != nil {
		s.logger.Errorw("could not publish video track", err)
		return nil, nil, err
	}

	s.logger.Debugw("published video track")

	return tracks, rtcpHandlers, nil
}

func (s *LKSDKOutput) AddOutputs(o ...SampleProvider) {
	s.lock.Lock()
	s.outputs = append(s.outputs, o...)
	s.lock.Unlock()
}

func (s *LKSDKOutput) closeOutput() {
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

func (s *LKSDKOutput) WriteRTCP(pkts []rtcp.Packet) error {
	if s.room == nil {
		return nil
	}

	if s.room.LocalParticipant == nil {
		return nil
	}

	pc := s.room.LocalParticipant.GetPublisherPeerConnection()
	if pc == nil {
		return nil
	}

	return pc.WriteRTCP(pkts)
}

func (s *LKSDKOutput) Close() error {
	s.closeOutput()

	var err error
	select {
	case err = <-s.errChan:
	default:
	}

	return err
}
