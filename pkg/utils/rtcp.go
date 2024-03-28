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

import "github.com/pion/rtcp"

func ReplaceRTCPPacketSSRC(pkt rtcp.Packet, newSSRC uint32) (rtcp.Packet, error) {
	switch cpkt := pkt.(type) {
	case *rtcp.SenderReport:
		cpkt.SSRC = newSSRC
	case *rtcp.ReceiverReport:
		cpkt.SSRC = newSSRC
	case *rtcp.SourceDescription:
		for i, _ := range cpkt.Chunks {
			cpkt.Chunks[i].Source = newSSRC
		}
	case *rtcp.Goodbye:
		for i, _ := range cpkt.Sources {
			cpkt.Sources[i] = newSSRC
		}
	case *rtcp.TransportLayerNack:
		cpkt.SenderSSRC = newSSRC
		cpkt.MediaSSRC = newSSRC
	case *rtcp.RapidResynchronizationRequest:
		cpkt.SenderSSRC = newSSRC
		cpkt.MediaSSRC = newSSRC
	case *rtcp.TransportLayerCC:
		cpkt.SenderSSRC = newSSRC
		cpkt.MediaSSRC = newSSRC
	case *rtcp.CCFeedbackReport:
		cpkt.SenderSSRC = newSSRC
	case *rtcp.PictureLossIndication:
		cpkt.SenderSSRC = newSSRC
		cpkt.MediaSSRC = newSSRC
	case *rtcp.SliceLossIndication:
		cpkt.SenderSSRC = newSSRC
		cpkt.MediaSSRC = newSSRC
	case *rtcp.ReceiverEstimatedMaximumBitrate:
		cpkt.SenderSSRC = newSSRC
	case *rtcp.FullIntraRequest:
		cpkt.SenderSSRC = newSSRC
		cpkt.MediaSSRC = newSSRC
		// TODO ExtendedReport
	}

	return pkt, nil
}
