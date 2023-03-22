package rtmp

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"path"
	"sync"

	"github.com/livekit/go-rtmp"
	rtmpmsg "github.com/livekit/go-rtmp/message"
	log "github.com/sirupsen/logrus"
	"github.com/yutopp/go-flv"
	flvtag "github.com/yutopp/go-flv/tag"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/logger"
)

type RTMPServer struct {
	server   *rtmp.Server
	handlers sync.Map
}

func NewRTMPServer() *RTMPServer {
	return &RTMPServer{}
}

func (s *RTMPServer) Start(conf *config.Config, onPublish func(streamKey string) error) error {
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
			h.OnPublishCallback(func(streamKey string) error {
				if onPublish != nil {
					err := onPublish(streamKey)
					if err != nil {
						return err
					}
				}

				s.handlers.Store(streamKey, h)

				return nil
			})
			h.OnCloseCallback(func(streamKey string) {
				s.handlers.Delete(streamKey)
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

func (s *RTMPServer) AssociateRelay(streamKey string, w io.WriteCloser) error {
	h, ok := s.handlers.Load(streamKey)
	if ok && h != nil {
		err := h.(*RTMPHandler).SetWriter(w)
		if err != nil {
			return err
		}
	} else {
		return errors.ErrIngressNotFound
	}

	return nil
}

func (s *RTMPServer) DissociateRelay(streamKey string) error {
	h, ok := s.handlers.Load(streamKey)
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

func (s *RTMPServer) Stop() error {
	return s.server.Close()
}

type RTMPHandler struct {
	rtmp.DefaultHandler

	flvEnc        *flv.Encoder
	streamKey     string
	videoInit     *flvtag.VideoData
	audioInit     *flvtag.AudioData
	keyFrameFound bool
	mediaBuffer   *prerollBuffer

	log logger.Logger

	onPublish func(streamKey string) error
	onClose   func(streamKey string)
}

func NewRTMPHandler() *RTMPHandler {
	h := &RTMPHandler{
		log: logger.GetLogger(),
	}

	h.mediaBuffer = newPrerollBuffer(func() error {
		h.log.Infow("preroll buffer reset event")
		h.flvEnc = nil

		return nil
	})

	return h
}

func (h *RTMPHandler) OnPublishCallback(cb func(streamKey string) error) {
	h.onPublish = cb
}

func (h *RTMPHandler) OnCloseCallback(cb func(streamKey string)) {
	h.onClose = cb
}

func (h *RTMPHandler) OnPublish(_ *rtmp.StreamContext, timestamp uint32, cmd *rtmpmsg.NetStreamPublish) error {
	// Reject a connection when PublishingName is empty
	if cmd.PublishingName == "" {
		return errors.ErrMissingStreamKey
	}

	// TODO check in store that PublishingName == stream key belongs to a valid ingress

	_, h.streamKey = path.Split(cmd.PublishingName)
	h.log = logger.GetLogger().WithValues("streamKey", h.streamKey)
	if h.onPublish != nil {
		err := h.onPublish(h.streamKey)
		if err != nil {
			return err
		}
	}

	h.log.Infow("Received a new published stream")

	return nil
}

func (h *RTMPHandler) OnSetDataFrame(timestamp uint32, data *rtmpmsg.NetStreamSetDataFrame) error {
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
		h.log.Errorw("failed to forward script data", err)
	}

	return nil
}

func (h *RTMPHandler) OnAudio(timestamp uint32, payload io.Reader) error {
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

	if h.audioInit == nil {
		h.audioInit = copyAudioTag(&audio)
	}

	if err := h.flvEnc.Encode(&flvtag.FlvTag{
		TagType:   flvtag.TagTypeAudio,
		Timestamp: timestamp,
		Data:      &audio,
	}); err != nil {
		// log and continue, or fail and let sender reconnect?
		h.log.Errorw("failed to write audio", err)
	}

	return nil
}

func (h *RTMPHandler) OnVideo(timestamp uint32, payload io.Reader) error {
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
		h.log.Errorw("Failed to write video", err)
	}

	return nil
}

func (h *RTMPHandler) OnClose() {
	h.log.Infow("closing ingress RTMP session")

	h.mediaBuffer.Close()

	if h.onClose != nil {
		h.onClose(h.streamKey)
	}
}

func (h *RTMPHandler) SetWriter(w io.WriteCloser) error {
	return h.mediaBuffer.setWriter(w)
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
