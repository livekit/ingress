package rtmp

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"sync"

	log "github.com/sirupsen/logrus"
	"github.com/yutopp/go-flv"
	flvtag "github.com/yutopp/go-flv/tag"
	"github.com/yutopp/go-rtmp"
	rtmpmsg "github.com/yutopp/go-rtmp/message"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/logger"
)

const (
	defaultRTMPPort int = 1935
)

type RTMPServer struct {
	server   *rtmp.Server
	handlers sync.Map
}

func NewRTMPServer() *RTMPServer {
	return &RTMPServer{}
}

func (s *RTMPServer) Start(conf *config.Config) error {
	port := conf.RTMPPort
	if port == 0 {
		port = defaultRTMPPort
	}

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

			h := NewRTMPHandler()
			h.OnPublishCallback(func(ingressId string) {
				s.handlers.Store(ingressId, h)
			})
			h.OnCloseCallback(func(ingressId string) {
				s.handlers.Delete(ingressId)
			})

			return conn, &rtmp.ConnConfig{
				Handler: h,

				ControlState: rtmp.StreamControlStateConfig{
					DefaultBandwidthWindowSize: 6 * 1024 * 1024 / 8,
				},

				Logger: l,
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

func (s *RTMPServer) AssociateRelay(ingressId string, w io.Writer) error {
	h, ok := s.handlers.Load(ingressId)
	if ok && h != nil {
		err := h.(*RTMPHandler).StartSerializer(w)
		if err != nil {
			return err
		}
	} else {
		return errors.ErrIngressNotFound
	}

	return nil
}

func (s *RTMPServer) DissociateRelay(ingressId string) error {
	h, ok := s.handlers.Load(ingressId)
	if ok && h != nil {
		h.(*RTMPHandler).StopSerializer()
	} else {
		return errors.ErrIngressNotFound
	}

	return nil
}

func (s *RTMPServer) Stop() error {
	return s.Stop()
}

type RTMPHandler struct {
	rtmp.DefaultHandler
	flvLock       sync.Mutex
	flvEnc        *flv.Encoder
	ingressId     string
	videoInit     *flvtag.VideoData
	keyFrameFound bool

	log logger.Logger

	onPublish func(ingressId string)
	onClose   func(ingressId string)
}

func NewRTMPHandler() *RTMPHandler {
	return &RTMPHandler{}
}

func (h *RTMPHandler) OnPublishCallback(cb func(ingressId string)) {
	h.onPublish = cb
}

func (h *RTMPHandler) OnCloseCallback(cb func(ingressId string)) {
	h.onClose = cb
}

func (h *RTMPHandler) OnPublish(_ *rtmp.StreamContext, timestamp uint32, cmd *rtmpmsg.NetStreamPublish) error {
	// Reject a connection when PublishingName is empty
	if cmd.PublishingName == "" {
		return errors.New("PublishingName is empty")
	}

	// TODO check in store that PublishingName == stream key belongs to a valid ingress

	h.ingressId = cmd.PublishingName
	h.log = logger.Logger(logger.GetLogger().WithValues("ingressID", cmd.PublishingName))
	if h.onPublish != nil {
		h.onPublish(h.ingressId)
	}

	h.log.Infow("Received a new published stream", "ingressID", cmd.PublishingName)

	return nil
}

func (h *RTMPHandler) OnSetDataFrame(timestamp uint32, data *rtmpmsg.NetStreamSetDataFrame) error {
	h.flvLock.Lock()
	defer h.flvLock.Unlock()

	if h.flvEnc != nil {
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
	}

	return nil
}

func (h *RTMPHandler) OnAudio(timestamp uint32, payload io.Reader) error {
	h.flvLock.Lock()
	defer h.flvLock.Unlock()

	if h.flvEnc != nil {
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

		if err := h.flvEnc.Encode(&flvtag.FlvTag{
			TagType:   flvtag.TagTypeAudio,
			Timestamp: timestamp,
			Data:      &audio,
		}); err != nil {
			// log and continue, or fail and let sender reconnect?
			h.log.Errorw("failed to write audio", err)
		}
	}

	return nil
}

func (h *RTMPHandler) OnVideo(timestamp uint32, payload io.Reader) error {
	h.flvLock.Lock()
	defer h.flvLock.Unlock()

	var video flvtag.VideoData
	if h.flvEnc != nil || h.videoInit == nil {
		if err := flvtag.DecodeVideoData(payload, &video); err != nil {
			return err
		}

		flvBody := new(bytes.Buffer)
		if _, err := io.Copy(flvBody, video.Data); err != nil {
			return err
		}
		video.Data = flvBody

		if h.videoInit == nil {
			h.videoInit = &video
		}
	}

	if h.flvEnc != nil {
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
	}

	return nil
}

func (h *RTMPHandler) OnClose() {
	h.log.Infow("closing ingress RTMP session")
	if h.onClose != nil {
		h.onClose(h.ingressId)
	}
}

func (h *RTMPHandler) StartSerializer(w io.Writer) error {
	h.flvLock.Lock()
	defer h.flvLock.Unlock()

	// Stop exsting session
	h.stopSerializer()

	enc, err := flv.NewEncoder(w, flv.FlagsAudio|flv.FlagsVideo)
	if err != nil {
		return err
	}
	h.flvEnc = enc

	// Serialize video decoder initialization
	if h.videoInit != nil {
		// Copy init tag to be able to reuse it

		if err := h.flvEnc.Encode(&flvtag.FlvTag{
			TagType:   flvtag.TagTypeVideo,
			Timestamp: 0,
			Data:      copyVideoTag(h.videoInit),
		}); err != nil {
			h.log.Errorw("Failed to write video", err)
		}
	}

	return nil
}

func (h *RTMPHandler) StopSerializer() {
	h.flvLock.Lock()
	defer h.flvLock.Unlock()

	h.stopSerializer()
}

func (h *RTMPHandler) stopSerializer() {
	h.log.Infow("stopping serializer")

	h.flvEnc = nil
	h.keyFrameFound = false
}

func copyVideoTag(in *flvtag.VideoData) *flvtag.VideoData {
	ret := *in
	ret.Data = bytes.NewReader(in.Data.(*bytes.Buffer).Bytes())

	return &ret
}
