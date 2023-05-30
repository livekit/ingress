package whip

import (
	"io"
	"time"

	"github.com/pion/webrtc/v3/pkg/media"

	"github.com/livekit/ingress/pkg/utils"
	"github.com/livekit/protocol/logger"
)

type RelayMediaSink struct {
	mediaBuffer *utils.PrerollBuffer
}

func NewRelayMediaSink(logger logger.Logger) *RelayMediaSink {
	mediaBuffer := utils.NewPrerollBuffer(func() error {
		logger.Infow("preroll buffer reset event")

		return nil
	})

	return &RelayMediaSink{
		mediaBuffer: mediaBuffer,
	}
}

func (rs *RelayMediaSink) PushSample(s *media.Sample, ts time.Duration) error {
	return utils.SerializeMediaForRelay(rs.mediaBuffer, s.Data, ts)
}

func (rs *RelayMediaSink) SetWriter(w io.WriteCloser) error {
	return rs.mediaBuffer.SetWriter(w)
}

func (rs *RelayMediaSink) Close() {
	rs.mediaBuffer.Close()
}
