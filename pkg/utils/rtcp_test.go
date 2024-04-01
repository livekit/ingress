// Copyright 2024 LiveKit, Inc.
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

package utils

import (
	"testing"

	"github.com/pion/rtcp"
	"github.com/stretchr/testify/require"
)

func TestReplaceRTCPPacketSSRC(t *testing.T) {
	t.Run("SenderReport", func(t *testing.T) {
		p := &rtcp.SenderReport{}

		r, err := ReplaceRTCPPacketSSRC(p, 1)

		require.NoError(t, err)
		require.Equal(t, uint32(1), p.SSRC)
		checkDestinationSSRC(t, r)
	})

	t.Run("ReceiverReport", func(t *testing.T) {
		p := &rtcp.ReceiverReport{}

		r, err := ReplaceRTCPPacketSSRC(p, 1)

		require.NoError(t, err)
		require.Equal(t, uint32(1), p.SSRC)
		checkDestinationSSRC(t, r)
	})

	t.Run("SourceDescription", func(t *testing.T) {
		p := &rtcp.SourceDescription{
			Chunks: []rtcp.SourceDescriptionChunk{
				rtcp.SourceDescriptionChunk{},
				rtcp.SourceDescriptionChunk{},
				rtcp.SourceDescriptionChunk{},
			},
		}

		r, err := ReplaceRTCPPacketSSRC(p, 1)

		require.NoError(t, err)
		require.Equal(t, 3, len(p.Chunks))
		for _, c := range p.Chunks {
			require.Equal(t, c.Source, uint32(1))
		}
		checkDestinationSSRC(t, r)
	})

	t.Run("Goodbye", func(t *testing.T) {
		p := &rtcp.Goodbye{
			Sources: []uint32{
				0,
				0,
				0,
			},
		}

		r, err := ReplaceRTCPPacketSSRC(p, 1)

		require.NoError(t, err)
		require.Equal(t, 3, len(p.Sources))
		for _, s := range p.Sources {
			require.Equal(t, uint32(1), s)
		}
		checkDestinationSSRC(t, r)
	})

	t.Run("TransportLayerNack", func(t *testing.T) {
		p := &rtcp.TransportLayerNack{}

		r, err := ReplaceRTCPPacketSSRC(p, 1)

		require.NoError(t, err)
		require.Equal(t, uint32(1), p.MediaSSRC)
		require.Equal(t, uint32(1), p.SenderSSRC)
		checkDestinationSSRC(t, r)
	})

	t.Run("RapidResynchronizationRequest", func(t *testing.T) {
		p := &rtcp.RapidResynchronizationRequest{}

		r, err := ReplaceRTCPPacketSSRC(p, 1)

		require.NoError(t, err)
		require.Equal(t, uint32(1), p.MediaSSRC)
		require.Equal(t, uint32(1), p.SenderSSRC)
		checkDestinationSSRC(t, r)
	})

	t.Run("TransportLayerCC", func(t *testing.T) {
		p := &rtcp.TransportLayerCC{}

		r, err := ReplaceRTCPPacketSSRC(p, 1)

		require.NoError(t, err)
		require.Equal(t, uint32(1), p.MediaSSRC)
		require.Equal(t, uint32(1), p.SenderSSRC)
		checkDestinationSSRC(t, r)
	})

	t.Run("CCFeedbackReport", func(t *testing.T) {
		p := &rtcp.CCFeedbackReport{}

		r, err := ReplaceRTCPPacketSSRC(p, 1)

		require.NoError(t, err)
		require.Equal(t, uint32(1), p.SenderSSRC)
		checkDestinationSSRC(t, r)
	})

	t.Run("PictureLossIndication", func(t *testing.T) {
		p := &rtcp.PictureLossIndication{}

		r, err := ReplaceRTCPPacketSSRC(p, 1)

		require.NoError(t, err)
		require.Equal(t, uint32(1), p.MediaSSRC)
		require.Equal(t, uint32(1), p.SenderSSRC)
		checkDestinationSSRC(t, r)
	})

	t.Run("SliceLossIndication", func(t *testing.T) {
		p := &rtcp.SliceLossIndication{}

		r, err := ReplaceRTCPPacketSSRC(p, 1)

		require.NoError(t, err)
		require.Equal(t, uint32(1), p.MediaSSRC)
		require.Equal(t, uint32(1), p.SenderSSRC)
		checkDestinationSSRC(t, r)
	})

	t.Run("ReceiverEstimatedMaximumBitrate", func(t *testing.T) {
		p := &rtcp.ReceiverEstimatedMaximumBitrate{}

		r, err := ReplaceRTCPPacketSSRC(p, 1)

		require.NoError(t, err)
		require.Equal(t, uint32(1), p.SenderSSRC)
		checkDestinationSSRC(t, r)
	})

	t.Run("FullIntraRequest", func(t *testing.T) {
		p := &rtcp.FullIntraRequest{}

		r, err := ReplaceRTCPPacketSSRC(p, 1)

		require.NoError(t, err)
		require.Equal(t, uint32(1), p.MediaSSRC)
		require.Equal(t, uint32(1), p.SenderSSRC)
		checkDestinationSSRC(t, r)
	})

	t.Run("ExtendedReport", func(t *testing.T) {
		p := &rtcp.ExtendedReport{
			Reports: []rtcp.ReportBlock{
				&rtcp.LossRLEReportBlock{},
				&rtcp.DuplicateRLEReportBlock{},
				&rtcp.DLRRReportBlock{
					Reports: []rtcp.DLRRReport{
						rtcp.DLRRReport{},
					},
				},
				&rtcp.StatisticsSummaryReportBlock{},
				&rtcp.VoIPMetricsReportBlock{},
			},
		}

		r, err := ReplaceRTCPPacketSSRC(p, 1)

		require.NoError(t, err)
		require.Equal(t, uint32(1), p.SenderSSRC)

		require.Equal(t, 6, len(p.DestinationSSRC()))
		checkDestinationSSRC(t, r)
	})
}

func checkDestinationSSRC(t *testing.T, p rtcp.Packet) {
	for _, d := range p.DestinationSSRC() {
		require.Equal(t, uint32(1), d)
	}
}
