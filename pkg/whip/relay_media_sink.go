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

package whip

import (
	"io"
	"time"

	"github.com/pion/webrtc/v4/pkg/media"

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

func (rs *RelayMediaSink) SetWriter(w io.WriteCloser) error {
	return rs.mediaBuffer.SetWriter(w)
}

func (rs *RelayMediaSink) Close() error {
	rs.mediaBuffer.Close()

	return nil
}

func (rs *RelayMediaSink) PushSample(s *media.Sample, ts time.Duration) error {
	return utils.SerializeMediaForRelay(rs.mediaBuffer, s.Data, ts)
}
