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
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/frostbyte73/core"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"golang.org/x/image/vp8"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/lksdk_output"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/ingress/pkg/utils"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/psrpc"
)

var (
	ErrParamsUnavailable = psrpc.NewErrorf(psrpc.InvalidArgument, "codec parameters unavailable in sample")
)

type SDKMediaSinkTrack struct {
	quality       livekit.VideoQuality
	width, height uint

	trackStatsGatherer *stats.MediaTrackStatGatherer
	localTrack         *lksdk_output.LocalTrack

	stateLock        sync.Mutex
	sendRTCPUpStream func(pkt rtcp.Packet)

	sink *SDKMediaSink
}

type SDKMediaSink struct {
	logger          logger.Logger
	params          *params.Params
	sdkOutput       *lksdk_output.LKSDKOutput
	sinkInitialized bool

	codecParameters webrtc.RTPCodecParameters
	streamKind      types.StreamKind

	tracksLock sync.Mutex
	tracks     map[livekit.VideoQuality]*SDKMediaSinkTrack

	fuse core.Fuse
}

func NewSDKMediaSink(
	l logger.Logger,
	p *params.Params,
	sdkOutput *lksdk_output.LKSDKOutput,
	codecParameters webrtc.RTPCodecParameters,
	streamKind types.StreamKind,
	layers []livekit.VideoQuality,
) *SDKMediaSink {
	s := &SDKMediaSink{
		logger:          l,
		params:          p,
		sdkOutput:       sdkOutput,
		tracks:          make(map[livekit.VideoQuality]*SDKMediaSinkTrack),
		streamKind:      streamKind,
		codecParameters: codecParameters,
	}

	for _, q := range layers {
		s.addTrack(q)
	}

	return s
}

func (sp *SDKMediaSink) GetTrack(quality livekit.VideoQuality) *SDKMediaSinkTrack {
	sp.tracksLock.Lock()
	defer sp.tracksLock.Unlock()

	return sp.tracks[quality]
}

func (sp *SDKMediaSink) Close() error {
	sp.fuse.Break()

	return nil
}

func (sp *SDKMediaSink) addTrack(quality livekit.VideoQuality) {
	sp.tracks[quality] = &SDKMediaSinkTrack{
		sink:    sp,
		quality: quality,
	}
}

func (sp *SDKMediaSink) ensureAudioTracksInitialized(pkt *rtp.Packet, t *SDKMediaSinkTrack) (bool, error) {
	stereo := strings.Contains(sp.codecParameters.SDPFmtpLine, "sprop-stereo=1")
	audioState := getAudioState(sp.codecParameters.MimeType, stereo, sp.codecParameters.ClockRate)
	sp.params.SetInputAudioState(context.Background(), audioState, true)

	sp.logger.Infow("adding audio track", "stereo", stereo, "codec", sp.codecParameters.MimeType)
	var err error
	t.localTrack, err = sp.sdkOutput.AddAudioTrack(sp.codecParameters.MimeType, false, stereo)
	if err != nil {
		return false, err
	}

	sp.sdkOutput.AddOutputs(t)

	sp.sinkInitialized = true
	return sp.sinkInitialized, nil
}

func (sp *SDKMediaSink) ensureVideoTracksInitialized(pkt *rtp.Packet, t *SDKMediaSinkTrack) (bool, error) {
	var err error
	width, height, err := getVideoParams(sp.codecParameters.MimeType, pkt)
	switch err {
	case nil:
		// continue
	case ErrParamsUnavailable:
		return false, nil
	default:
		return false, err
	}

	t.width = width
	t.height = height

	layers := []*livekit.VideoLayer{}
	sbArray := []lksdk_output.SampleProvider{}

	for _, track := range sp.tracks {
		if track.width != 0 && track.height != 0 {
			layers = append(layers, &livekit.VideoLayer{
				Width:   uint32(track.width),
				Height:  uint32(track.height),
				Quality: track.quality,
			})
		}
		sbArray = append(sbArray, track)
	}

	// Simulcast
	if len(sp.tracks) > 1 {
		if len(layers) != len(sp.tracks) {
			return false, nil
		}
	} else {
		// Non-simulcast
		if len(layers) != 1 {
			return false, nil
		}

	}

	sort.Slice(layers, func(i, j int) bool {
		return layers[i].Width > layers[j].Width
	})

	if len(layers) != 0 {
		videoState := getVideoState(sp.codecParameters.MimeType, uint(layers[0].Width), uint(layers[0].Height))
		sp.params.SetInputVideoState(context.Background(), videoState, true)
	}

	tracks, rtcpHandlers, err := sp.sdkOutput.AddVideoTrack(layers, sp.codecParameters.MimeType)
	if err != nil {
		return false, err
	}

	sp.sdkOutput.AddOutputs(sbArray...)

	for i, q := range layers {
		t = sp.tracks[q.Quality]
		t.localTrack = tracks[i]
		rtcpHandlers[i].SetPacketSink(t)
		sp.logger.Infow("adding video track", "width", q.Width, "height", q.Height, "quality", q.Quality, "codec", sp.codecParameters.MimeType)
	}

	sp.sinkInitialized = true

	return sp.sinkInitialized, nil

}

func (sp *SDKMediaSink) ensureTracksInitialized(pkt *rtp.Packet, t *SDKMediaSinkTrack) (bool, error) {
	if sp.sinkInitialized {
		return sp.sinkInitialized, nil
	}

	if sp.streamKind == types.Audio {
		return sp.ensureAudioTracksInitialized(pkt, t)
	}

	return sp.ensureVideoTracksInitialized(pkt, t)
}

func (t *SDKMediaSinkTrack) PushRTP(pkt *rtp.Packet) error {
	if t.sink.fuse.IsBroken() {
		return io.EOF
	}

	t.sink.tracksLock.Lock()

	tracksInitialized, err := t.sink.ensureTracksInitialized(pkt, t)
	if err != nil {
		t.sink.tracksLock.Unlock()
		return err
	} else if !tracksInitialized {
		// Drop the sample
		t.sink.tracksLock.Unlock()
		return nil
	}
	g := t.trackStatsGatherer
	localTrack := t.localTrack
	t.sink.tracksLock.Unlock()

	if localTrack == nil {
		if g != nil {
			g.PacketLost(1)
		}

		return nil
	}

	// WriteSample seems to return successfully even if the Peer Connection disconnected.
	// We need to return success to the caller even if the PC is disconnected to allow for reconnections
	err = localTrack.WriteRTP(pkt, nil)
	if err != nil {
		return err
	}

	if g != nil {
		g.MediaReceived(int64(len(pkt.Payload)))
	}

	return nil
}

func (t *SDKMediaSinkTrack) HandleRTCPPacket(pkt rtcp.Packet) error {
	// LK SDK -> WHIP RTCP handling
	t.stateLock.Lock()
	defer t.stateLock.Unlock()

	if t.sendRTCPUpStream != nil {
		t.sendRTCPUpStream(pkt)
	}

	return nil
}

func (t *SDKMediaSinkTrack) PushRTCP(pkts []rtcp.Packet) error {
	// WHIP -> LK SDK RTCP handling

	t.sink.tracksLock.Lock()
	localTrack := t.localTrack
	t.sink.tracksLock.Unlock()

	if localTrack == nil {
		return nil
	}

	ssrc := localTrack.SSRC()
	if ssrc == 0 {
		return nil
	}

	for _, pkt := range pkts {
		utils.ReplaceRTCPPacketSSRC(pkt, uint32(ssrc))
	}

	err := t.sink.sdkOutput.WriteRTCP(pkts)
	if err != nil {
		return err
	}

	return nil
}

func (sp *SDKMediaSinkTrack) QueueLength() int {
	// No buffering
	return 0
}

func (t *SDKMediaSinkTrack) Close() error {
	return t.sink.Close()
}

func (t *SDKMediaSinkTrack) SetStatsGatherer(st *stats.LocalMediaStatsGatherer) {
	var path string
	switch t.sink.streamKind {
	case types.Audio:
		path = stats.OutputAudio
	case types.Video:
		path = fmt.Sprintf("%s.%s", stats.OutputVideo, t.quality)
	default:
		path = "output.unknown"
	}

	g := st.RegisterTrackStats(path)

	t.sink.tracksLock.Lock()
	t.trackStatsGatherer = g
	t.sink.tracksLock.Unlock()
}

func (t *SDKMediaSinkTrack) SetRTCPPacketSink(handleRTCPPacket func(pkt rtcp.Packet)) {
	t.stateLock.Lock()
	defer t.stateLock.Unlock()

	t.sendRTCPUpStream = handleRTCPPacket
}

func getVideoParams(mimeType string, pkt *rtp.Packet) (uint, uint, error) {
	switch strings.ToLower(mimeType) {
	case strings.ToLower(webrtc.MimeTypeH264):
		return getH264VideoParams(pkt)
	case strings.ToLower(webrtc.MimeTypeVP8):
		return getVP8VideoParams(pkt)
	default:
		return 0, 0, errors.ErrUnsupportedDecodeMimeType(mimeType)
	}
}

func getH264VideoParams(pkt *rtp.Packet) (uint, uint, error) {
	depacketizer := &codecs.H264Packet{}

	b, err := depacketizer.Unmarshal(pkt.Payload)
	if err != nil {
		return 0, 0, err
	}

	var spss [][]byte
	if isAnnexB(b) {
		spss = avc.ExtractNalusOfTypeFromByteStream(avc.NALU_SPS, b, true)
	} else {
		spss, _ = avc.GetParameterSets(b)
	}

	if len(spss) == 0 {
		return 0, 0, ErrParamsUnavailable
	}

	sps, err := avc.ParseSPSNALUnit(spss[0], false)
	if err != nil {
		return 0, 0, err
	}

	return sps.Width, sps.Height, nil
}

func isAnnexB(b []byte) bool {
	for i := 0; i < len(b)-3; i++ {
		if b[i] != 0 {
			return false
		}
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			return true
		}
	}

	return false
}

func getVP8VideoParams(pkt *rtp.Packet) (uint, uint, error) {
	depacketizer := &codecs.VP8Packet{}

	b, err := depacketizer.Unmarshal(pkt.Payload)

	d := vp8.NewDecoder()
	r := bytes.NewReader(b)

	d.Init(r, r.Len())
	fh, err := d.DecodeFrameHeader()
	if err != nil {
		return 0, 0, err
	}

	return uint(fh.Width), uint(fh.Height), nil
}

func getAudioState(mimeType string, stereo bool, samplerate uint32) *livekit.InputAudioState {
	channels := uint32(1)
	if stereo {
		channels = 2
	}

	return &livekit.InputAudioState{
		MimeType:   mimeType,
		Channels:   channels,
		SampleRate: samplerate,
	}
}

func getVideoState(mimeType string, w uint, h uint) *livekit.InputVideoState {
	return &livekit.InputVideoState{
		MimeType: mimeType,
		Width:    uint32(w),
		Height:   uint32(h),
	}
}
