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
	server  *rtmp.Server
	writers sync.Map
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

			h := NewRTMPHandler(func(ingressId string, w io.Writer) {
				s.writers.Store(ingressId, w)
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
	if err := srv.Serve(listener); err != nil {
		logger.Errorw("failed to start RTMP server", err)
		return err
	}

	s.server = srv

	return nil
}

func (s *RTMPServer) AssociateRelay(ingressId string, p io.Writer) error {
	w, ok := s.writers.Load(ingressId)
	if ok && w != nil {
		w.(*WrappingWriter).SetWriter(p)
	} else {
		return errors.ErrIngressNotFound
	}

	return nil
}

func (s *RTMPServer) DissociateRelay(ingressId string) error {
	w, ok := s.writers.Load(ingressId)
	if ok && w != nil {
		w.(*WrappingWriter).SetWriter(nil)
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
	flvEnc    *flv.Encoder
	ingressId string
	log       logger.Logger

	onPublish func(ingressId string, w io.Writer)
}

func NewRTMPHandler(onPublish func(ingressId string, w io.Writer)) *RTMPHandler {
	return &RTMPHandler{
		onPublish: onPublish,
	}
}

func (h *RTMPHandler) OnPublish(_ *rtmp.StreamContext, timestamp uint32, cmd *rtmpmsg.NetStreamPublish) error {
	// Reject a connection when PublishingName is empty
	if cmd.PublishingName == "" {
		return errors.New("PublishingName is empty")
	}

	// TODO check in store that PublishingName == stream key belongs to a valid ingress

	h.ingressId = cmd.PublishingName
	h.log = logger.Logger(logger.GetLogger().WithValues("ingressID", cmd.PublishingName))

	h.log.Infow("Received a new published stream", "ingressID", cmd.PublishingName)

	w := &WrappingWriter{}
	h.onPublish(h.ingressId, w)

	enc, err := flv.NewEncoder(w, flv.FlagsAudio|flv.FlagsVideo)
	if err != nil {
		return err
	}
	h.flvEnc = enc

	return nil
}

func (h *RTMPHandler) OnSetDataFrame(timestamp uint32, data *rtmpmsg.NetStreamSetDataFrame) error {
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

	return nil
}

func (h *RTMPHandler) OnVideo(timestamp uint32, payload io.Reader) error {
	var video flvtag.VideoData
	if err := flvtag.DecodeVideoData(payload, &video); err != nil {
		return err
	}

	flvBody := new(bytes.Buffer)
	if _, err := io.Copy(flvBody, video.Data); err != nil {
		return err
	}
	video.Data = flvBody

	// Should we drop messages up to the 1st key frame, or will GST do so?
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
}

type WrappingWriter struct {
	w    io.Writer
	lock sync.Mutex
}

func (w *WrappingWriter) Write(b []byte) (int, error) {
	w.lock.Lock()
	wr := w.w
	w.lock.Unlock()

	if wr == nil {
		return len(b), nil
	}

	return w.Write(b)
}

func (w *WrappingWriter) SetWriter(wr io.Writer) {
	w.lock.Lock()
	w.w = wr
	w.lock.Unlock()
}
