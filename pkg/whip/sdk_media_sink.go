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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/frostbyte73/core"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"golang.org/x/image/vp8"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/lksdk_output"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/psrpc"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

var (
	ErrParamsUnavailable = psrpc.NewErrorf(psrpc.InvalidArgument, "codec parameters unavailable in sample")
)

type SDKMediaSinkTrack struct {
	quality       livekit.VideoQuality
	width, height uint

	trackStatsGatherer *stats.MediaTrackStatGatherer
	localTrack         *lksdk.LocalTrack
	bound              atomic.Bool

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

func (sp *SDKMediaSink) ensureAudioTracksInitialized(s *media.Sample, t *SDKMediaSinkTrack) (bool, error) {
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

func (sp *SDKMediaSink) ensureVideoTracksInitialized(s *media.Sample, t *SDKMediaSinkTrack) (bool, error) {
	var err error
	t.width, t.height, err = getVideoParams(sp.codecParameters.MimeType, s)
	switch err {
	case nil:
		// continue
	case ErrParamsUnavailable:
		return false, nil
	default:
		return false, err
	}

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
		rtcpHandlers[i].SetKeyFrameEmitter(t)
	}

	for _, l := range layers {
		sp.logger.Infow("adding video track", "width", l.Width, "height", l.Height, "codec", sp.codecParameters.MimeType)
	}
	sp.sinkInitialized = true

	return sp.sinkInitialized, nil

}

func (sp *SDKMediaSink) ensureTracksInitialized(s *media.Sample, t *SDKMediaSinkTrack) (bool, error) {
	if sp.sinkInitialized {
		return sp.sinkInitialized, nil
	}

	if sp.streamKind == types.Audio {
		return sp.ensureAudioTracksInitialized(s, t)
	}

	return sp.ensureVideoTracksInitialized(s, t)
}

func (t *SDKMediaSinkTrack) PushSample(s *media.Sample, ts time.Duration) error {
	if t.sink.fuse.IsBroken() {
		return io.EOF
	}

	t.sink.tracksLock.Lock()

	tracksInitialized, err := t.sink.ensureTracksInitialized(s, t)
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
	err = localTrack.WriteSample(*s, nil)
	if err != nil {
		return err
	}

	if g != nil {
		g.MediaReceived(int64(len(s.Data)))
	}

	return nil
}

func (t *SDKMediaSinkTrack) PushRTCP(pkts []rtcp.Packet) error {
	t.translateRTCPPackets(pkts)

	if !t.bound.Load() {
		return nil
	}

	err := t.sink.sdkOutput.WriteRTCP(pkts)
	// Write can fail if the pc is in a disconnected/failing and the track hasn't been unbound yet. The "bound" check is also racy.
	// Do not fail if RTCP writing failed
	if err != nil {
		t.sink.logger.Infow("RTCP write failed", "error", err)
	}

	retutn nil
}

func (t *SDKMediaSinkTrack) Close() error {
	return t.sink.Close()
}

func (t *SDKMediaSinkTrack) OnBind() error {
	t.sink.logger.Infow("media sink bound")

	t.bound.Store(true)

	return nil
}

func (t *SDKMediaSinkTrack) OnUnbind() error {
	t.sink.logger.Infow("media sink unbound")

	t.bound.Store(false)

	return nil
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

func getVideoParams(mimeType string, s *media.Sample) (uint, uint, error) {
	switch strings.ToLower(mimeType) {
	case strings.ToLower(webrtc.MimeTypeH264):
		return getH264VideoParams(s)
	case strings.ToLower(webrtc.MimeTypeVP8):
		return getVP8VideoParams(s)
	default:
		return 0, 0, errors.ErrUnsupportedDecodeMimeType(mimeType)
	}
}

func getH264VideoParams(s *media.Sample) (uint, uint, error) {
	spss := avc.ExtractNalusOfTypeFromByteStream(avc.NALU_SPS, s.Data, true)
	if len(spss) == 0 {
		return 0, 0, ErrParamsUnavailable
	}

	sps, err := avc.ParseSPSNALUnit(spss[0], false)
	if err != nil {
		return 0, 0, err
	}

	return sps.Width, sps.Height, nil
}

func getVP8VideoParams(s *media.Sample) (uint, uint, error) {
	d := vp8.NewDecoder()
	b := bytes.NewReader(s.Data)

	d.Init(b, b.Len())
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
