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

package rtmp

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"path"
	"sync"

	"github.com/frostbyte73/core"
	"github.com/livekit/go-rtmp"
	rtmpmsg "github.com/livekit/go-rtmp/message"
	log "github.com/sirupsen/logrus"
	"github.com/yutopp/go-flv"
	flvtag "github.com/yutopp/go-flv/tag"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/ingress/pkg/utils"
	"github.com/livekit/protocol/logger"
	protoutils "github.com/livekit/protocol/utils"
)

type RTMPServer struct {
	server   *rtmp.Server
	handlers sync.Map
}

func NewRTMPServer() *RTMPServer {
	return &RTMPServer{}
}

func (s *RTMPServer) Start(conf *config.Config, onPublish func(streamKey, resourceId string) (*params.Params, *stats.LocalMediaStatsGatherer, error)) error {
	port := conf.RTMPPort

	tcpAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}

	listener, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		logger.Errorw("failed to start TCP listener", err, "port", port)
		return err
	}

	srv := rtmp.NewServer(&rtmp.ServerConfig{
		OnConnect: func(conn net.Conn) (io.ReadWriteCloser, *rtmp.ConnConfig) {
			// Should we find a way to use our own logger?
			l := log.StandardLogger()
			if conf.Logging.JSON {
				l.SetFormatter(&log.JSONFormatter{})
			}
			lf := l.WithFields(conf.GetLoggerFields())

			h := NewRTMPHandler()
			h.OnPublishCallback(func(streamKey, resourceId string) (*params.Params, *stats.LocalMediaStatsGatherer, error) {
				var params *params.Params
				var stats *stats.LocalMediaStatsGatherer
				var err error
				if onPublish != nil {
					params, stats, err = onPublish(streamKey, resourceId)
					if err != nil {
						return nil, nil, err
					}
				}

				s.handlers.Store(resourceId, h)

				return params, stats, nil
			})
			h.OnCloseCallback(func(resourceId string) {
				s.handlers.Delete(resourceId)
			})

			return conn, &rtmp.ConnConfig{
				Handler: h,

				ControlState: rtmp.StreamControlStateConfig{
					DefaultBandwidthWindowSize: 6 * 1024 * 1024 / 8,
				},

				Logger: lf,
			}
		},
	})

	s.server = srv
	go func() {
		if err := srv.Serve(listener); err != nil {
			logger.Errorw("failed to start RTMP server", err)
		}
	}()

	return nil
}

func (s *RTMPServer) AssociateRelay(resourceId string, token string, w io.WriteCloser) error {
	h, ok := s.handlers.Load(resourceId)
	if ok && h != nil {
		if h.(*RTMPHandler).params.RelayToken != token {
			return errors.ErrInvalidRelayToken
		}

		err := h.(*RTMPHandler).SetWriter(w)
		if err != nil {
			return err
		}
	} else {
		return errors.ErrIngressNotFound
	}

	return nil
}

func (s *RTMPServer) DissociateRelay(resourceId string) error {
	h, ok := s.handlers.Load(resourceId)
	if ok && h != nil {
		err := h.(*RTMPHandler).SetWriter(nil)
		if err != nil {
			return err
		}
	} else {
		return errors.ErrIngressNotFound
	}

	return nil
}

func (s *RTMPServer) CloseHandler(resourceId string) {
	h, ok := s.handlers.Load(resourceId)
	if ok && h != nil {
		h.(*RTMPHandler).Close()
	}
}

func (s *RTMPServer) Stop() error {
	return s.server.Close()
}

type RTMPHandler struct {
	rtmp.DefaultHandler

	flvEnc        *flv.Encoder
	trackStats    map[types.StreamKind]*stats.MediaTrackStatGatherer
	params        *params.Params
	resourceId    string
	videoInit     *flvtag.VideoData
	audioInit     *flvtag.AudioData
	keyFrameFound bool
	mediaBuffer   *utils.PrerollBuffer

	log    logger.Logger
	closed core.Fuse

	onPublish func(streamKey, resourceId string) (*params.Params, *stats.LocalMediaStatsGatherer, error)
	onClose   func(resourceId string)
}

func NewRTMPHandler() *RTMPHandler {
	h := &RTMPHandler{
		log:        logger.GetLogger(),
		trackStats: make(map[types.StreamKind]*stats.MediaTrackStatGatherer),
	}

	h.mediaBuffer = utils.NewPrerollBuffer(func() error {
		h.log.Infow("preroll buffer reset event")
		h.flvEnc = nil

		return nil
	})

	return h
}

func (h *RTMPHandler) OnPublishCallback(cb func(streamKey, resourceId string) (*params.Params, *stats.LocalMediaStatsGatherer, error)) {
	h.onPublish = cb
}

func (h *RTMPHandler) OnCloseCallback(cb func(resourceId string)) {
	h.onClose = cb
}

func (h *RTMPHandler) OnPublish(_ *rtmp.StreamContext, timestamp uint32, cmd *rtmpmsg.NetStreamPublish) error {
	// Reject a connection when PublishingName is empty
	if cmd.PublishingName == "" {
		return errors.ErrMissingStreamKey
	}

	_, streamKey := path.Split(cmd.PublishingName)
	h.resourceId = protoutils.NewGuid(protoutils.RTMPResourcePrefix)
	h.log = logger.GetLogger().WithValues("streamKey", streamKey, "resourceID", h.resourceId)
	if h.onPublish != nil {
		params, st, err := h.onPublish(streamKey, h.resourceId)
		if err != nil {
			return err
		}
		h.params = params

		h.trackStats[types.Audio] = st.RegisterTrackStats(stats.InputAudio)
		h.trackStats[types.Video] = st.RegisterTrackStats(stats.InputVideo)
	}

	h.log.Infow("Received a new published stream")

	return nil
}

func (h *RTMPHandler) OnSetDataFrame(timestamp uint32, data *rtmpmsg.NetStreamSetDataFrame) error {
	if h.closed.IsBroken() {
		return io.EOF
	}

	if h.flvEnc == nil {
		err := h.initFlvEncoder()
		if err != nil {
			return err
		}
	}
	r := bytes.NewReader(data.Payload)

	var script flvtag.ScriptData
	if err := flvtag.DecodeScriptData(r, &script); err != nil {
		h.log.Errorw("failed to decode script data", err)
		return nil // ignore
	}

	if err := h.flvEnc.Encode(&flvtag.FlvTag{
		TagType:   flvtag.TagTypeScriptData,
		Timestamp: timestamp,
		Data:      &script,
	}); err != nil {
		h.log.Warnw("failed to forward script data", err)
		return err
	}

	return nil
}

func (h *RTMPHandler) OnAudio(timestamp uint32, payload io.Reader) error {
	if h.closed.IsBroken() {
		return io.EOF
	}

	if h.flvEnc == nil {
		err := h.initFlvEncoder()
		if err != nil {
			return err
		}
	}

	var audio flvtag.AudioData
	if err := flvtag.DecodeAudioData(payload, &audio); err != nil {
		return err
	}

	// Why copy the payload here?
	flvBody := new(bytes.Buffer)
	if _, err := io.Copy(flvBody, audio.Data); err != nil {
		return err
	}
	audio.Data = flvBody

	if st := h.trackStats[types.Audio]; st != nil {
		st.MediaReceived(int64(flvBody.Len()))
	}

	if h.audioInit == nil {
		h.audioInit = copyAudioTag(&audio)
	}

	if err := h.flvEnc.Encode(&flvtag.FlvTag{
		TagType:   flvtag.TagTypeAudio,
		Timestamp: timestamp,
		Data:      &audio,
	}); err != nil {
		h.log.Warnw("failed to write audio", err)
		return err
	}

	return nil
}

func (h *RTMPHandler) OnVideo(timestamp uint32, payload io.Reader) error {
	if h.closed.IsBroken() {
		return io.EOF
	}

	if h.flvEnc == nil {
		err := h.initFlvEncoder()
		if err != nil {
			return err
		}
	}

	var video flvtag.VideoData
	if err := flvtag.DecodeVideoData(payload, &video); err != nil {
		return err
	}

	flvBody := new(bytes.Buffer)
	if _, err := io.Copy(flvBody, video.Data); err != nil {
		return err
	}
	video.Data = flvBody

	if st := h.trackStats[types.Video]; st != nil {
		st.MediaReceived(int64(flvBody.Len()))
	}

	if h.videoInit == nil {
		h.videoInit = copyVideoTag(&video)
	}

	if !h.keyFrameFound {
		if video.FrameType == flvtag.FrameTypeKeyFrame {
			h.log.Infow("key frame found")
			h.keyFrameFound = true
		} else {
			return nil
		}
	}

	if err := h.flvEnc.Encode(&flvtag.FlvTag{
		TagType:   flvtag.TagTypeVideo,
		Timestamp: timestamp,
		Data:      &video,
	}); err != nil {
		h.log.Warnw("Failed to write video", err)
		return err
	}

	return nil
}

func (h *RTMPHandler) OnClose() {
	h.log.Infow("closing ingress RTMP session")

	h.mediaBuffer.Close()

	if h.onClose != nil {
		h.onClose(h.resourceId)
	}
}

func (h *RTMPHandler) SetWriter(w io.WriteCloser) error {
	return h.mediaBuffer.SetWriter(w)
}

func (h *RTMPHandler) Close() {
	h.closed.Break()
}

func (h *RTMPHandler) initFlvEncoder() error {
	h.keyFrameFound = false
	enc, err := flv.NewEncoder(h.mediaBuffer, flv.FlagsAudio|flv.FlagsVideo)
	if err != nil {
		return err
	}
	h.flvEnc = enc

	// Serialize video and video decoder initialization
	if h.videoInit != nil {
		// Copy init tag to be able to reuse it

		if err := h.flvEnc.Encode(&flvtag.FlvTag{
			TagType:   flvtag.TagTypeVideo,
			Timestamp: 0,
			Data:      copyVideoTag(h.videoInit),
		}); err != nil {
			return err
		}
	}
	if h.audioInit != nil {
		// Copy init tag to be able to reuse it

		if err := h.flvEnc.Encode(&flvtag.FlvTag{
			TagType:   flvtag.TagTypeAudio,
			Timestamp: 0,
			Data:      copyAudioTag(h.audioInit),
		}); err != nil {
			return err
		}
	}

	return nil
}

func copyVideoTag(in *flvtag.VideoData) *flvtag.VideoData {
	ret := *in
	ret.Data = bytes.NewBuffer(in.Data.(*bytes.Buffer).Bytes())

	return &ret
}

func copyAudioTag(in *flvtag.AudioData) *flvtag.AudioData {
	ret := *in
	ret.Data = bytes.NewBuffer(in.Data.(*bytes.Buffer).Bytes())

	return &ret
}
