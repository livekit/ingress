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
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/frostbyte73/core"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
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
	lksdk "github.com/livekit/server-sdk-go"
)

var (
	ErrParamsUnavailable = psrpc.NewErrorf(psrpc.InvalidArgument, "codec parameters unavailable in sample")
)

type SDKMediaSink struct {
	logger             logger.Logger
	params             *params.Params
	writePLI           func()
	track              *webrtc.TrackRemote
	outputSync         *utils.TrackOutputSynchronizer
	trackStatsGatherer atomic.Pointer[stats.MediaTrackStatGatherer]
	sdkOutput          *lksdk_output.LKSDKOutput

	localTrack *lksdk.LocalTrack

	fuse             core.Fuse
	trackInitialized bool
}

type sample struct {
	s  *media.Sample
	ts time.Duration
}

func NewSDKMediaSink(l logger.Logger, p *params.Params, sdkOutput *lksdk_output.LKSDKOutput, track *webrtc.TrackRemote, outputSync *utils.TrackOutputSynchronizer, writePLI func()) *SDKMediaSink {
	s := &SDKMediaSink{
		logger:     l,
		params:     p,
		writePLI:   writePLI,
		track:      track,
		outputSync: outputSync,
		sdkOutput:  sdkOutput,
		fuse:       core.NewFuse(),
	}

	return s
}

func (sp *SDKMediaSink) PushSample(s *media.Sample, ts time.Duration) error {
	if sp.fuse.IsBroken() {
		return io.EOF
	}

	err := sp.ensureTrackInitialized(s)
	if err != nil {
		return err
	}
	if !sp.trackInitialized {
		// Drop the sample
		return nil
	}

	g := sp.trackStatsGatherer.Load()

	// Synchronize the outputs before the network jitter buffer to avoid old samples stuck
	// in the channel from increasing the whole pipeline delay.
	drop, err := sp.outputSync.WaitForMediaTime(ts)
	if err != nil {
		return err
	}
	if drop {
		sp.logger.Debugw("dropping sample", "timestamp", ts)

		if g != nil {
			g.PacketLost(1)
		}

		return nil
	}

	err = sp.localTrack.WriteSample(*s, nil)
	if err != nil {
		if g != nil {
			g.PacketLost(1)
		}

		return nil
	}

	if g != nil {
		g.MediaReceived(int64(len(s.Data)))
	}

	return nil
}

func (sp *SDKMediaSink) SetStatsGatherer(st *stats.LocalMediaStatsGatherer) {
	var path string
	switch sp.track.Kind() {
	case webrtc.RTPCodecTypeAudio:
		path = stats.OutputAudio
	case webrtc.RTPCodecTypeVideo:
		path = stats.OutputVideo
	default:
		path = "output.unknown"
	}

	g := st.RegisterTrackStats(path)

	sp.trackStatsGatherer.Store(g)
}

func (sp *SDKMediaSink) OnBind() error {
	sp.logger.Infow("media sink bound")

	return nil
}

func (sp *SDKMediaSink) OnUnbind() error {
	sp.logger.Infow("media sink unbound")

	return nil
}

func (sp *SDKMediaSink) ForceKeyFrame() error {
	if sp.writePLI != nil {
		sp.writePLI()
	}

	return nil
}

func (sp *SDKMediaSink) SetWriter(w io.WriteCloser) error {
	return psrpc.Unimplemented
}

func (sp *SDKMediaSink) Close() error {
	sp.fuse.Break()
	sp.outputSync.Close()

	return nil
}

func (sp *SDKMediaSink) ensureTrackInitialized(s *media.Sample) error {
	if sp.trackInitialized {
		return nil
	}

	kind := streamKindFromCodecType(sp.track.Kind())
	mimeType := sp.track.Codec().MimeType

	switch kind {
	case types.Audio:
		stereo := parseAudioFmtp(sp.track.Codec().SDPFmtpLine)
		audioState := getAudioState(sp.track.Codec().MimeType, stereo, sp.track.Codec().ClockRate)
		sp.params.SetInputAudioState(context.Background(), audioState, true)

		sp.logger.Infow("adding audio track", "stereo", stereo, "codec", mimeType)
		track, err := sp.sdkOutput.AddAudioTrack(mimeType, false, stereo)
		if err != nil {
			return err
		}
		sp.localTrack = track

	case types.Video:
		w, h, err := getVideoParams(mimeType, s)
		switch err {
		case nil:
			// continue
		case ErrParamsUnavailable:
			return nil
		default:
			return err
		}

		layers := []*livekit.VideoLayer{
			&livekit.VideoLayer{Width: uint32(w), Height: uint32(h), Quality: livekit.VideoQuality_HIGH},
		}

		videoState := getVideoState(sp.track.Codec().MimeType, w, h)
		sp.params.SetInputVideoState(context.Background(), videoState, true)

		sp.logger.Infow("adding video track", "width", w, "height", h, "codec", mimeType)
		tracks, pliHandlers, err := sp.sdkOutput.AddVideoTrack(layers, mimeType)

		pliHandlers[0].SetKeyFrameEmitter(sp)
		sp.localTrack = tracks[0]
	}

	sp.sdkOutput.AddOutputs(sp)

	sp.trackInitialized = true

	return nil
}

func parseAudioFmtp(audioFmtp string) bool {
	return strings.Index(audioFmtp, "sprop-stereo=1") >= 0
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
