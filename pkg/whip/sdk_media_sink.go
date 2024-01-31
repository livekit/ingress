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
	"sync"
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
)

var (
	ErrParamsUnavailable = psrpc.NewErrorf(psrpc.InvalidArgument, "codec parameters unavailable in sample")
)

type SDKMediaSinkTrack struct {
	readySamples chan *sample
	writePLI     func()

	quality       livekit.VideoQuality
	width, height uint

	sink *SDKMediaSink
}

type SDKMediaSink struct {
	logger             logger.Logger
	params             *params.Params
	outputSync         *utils.TrackOutputSynchronizer
	trackStatsGatherer atomic.Pointer[stats.MediaTrackStatGatherer]
	sdkOutput          *lksdk_output.LKSDKOutput
	sinkInitialized    bool

	codecParameters webrtc.RTPCodecParameters
	streamKind      types.StreamKind

	tracksLock sync.Mutex
	tracks     []*SDKMediaSinkTrack

	fuse core.Fuse
}

type sample struct {
	s  *media.Sample
	ts time.Duration
}

func NewSDKMediaSink(
	l logger.Logger, p *params.Params, sdkOutput *lksdk_output.LKSDKOutput,
	codecParameters webrtc.RTPCodecParameters, streamKind types.StreamKind,
	outputSync *utils.TrackOutputSynchronizer,
) *SDKMediaSink {
	return &SDKMediaSink{
		logger:          l,
		params:          p,
		outputSync:      outputSync,
		sdkOutput:       sdkOutput,
		fuse:            core.NewFuse(),
		tracks:          []*SDKMediaSinkTrack{},
		streamKind:      streamKind,
		codecParameters: codecParameters,
	}
}

func (sp *SDKMediaSink) AddTrack(quality livekit.VideoQuality) {
	sp.tracksLock.Lock()
	defer sp.tracksLock.Unlock()

	sp.tracks = append(sp.tracks, &SDKMediaSinkTrack{
		readySamples: make(chan *sample, 15),
		sink:         sp,
		quality:      quality,
	})
}

func (sp *SDKMediaSink) SetWritePLI(quality livekit.VideoQuality, writePLI func()) *SDKMediaSinkTrack {
	sp.tracksLock.Lock()
	defer sp.tracksLock.Unlock()

	for i := range sp.tracks {
		if sp.tracks[i].quality == quality {
			sp.tracks[i].writePLI = writePLI
			return sp.tracks[i]
		}
	}

	return nil
}

func (t *SDKMediaSinkTrack) SetStatsGatherer(st *stats.LocalMediaStatsGatherer) {
	var path string
	switch t.sink.streamKind {
	case types.Audio:
		path = stats.OutputAudio
	case types.Video:
		path = stats.OutputVideo
	default:
		path = "output.unknown"
	}

	g := st.RegisterTrackStats(path)

	t.sink.trackStatsGatherer.Store(g)
}

func (sp *SDKMediaSink) Close() error {
	sp.fuse.Break()
	sp.outputSync.Close()

	return nil
}

func (sp *SDKMediaSink) ensureAudioTracksInitialized(s *media.Sample, t *SDKMediaSinkTrack) (bool, error) {
	stereo := strings.Contains(sp.codecParameters.SDPFmtpLine, "sprop-stereo=1")
	audioState := getAudioState(sp.codecParameters.MimeType, stereo, sp.codecParameters.ClockRate)
	sp.params.SetInputAudioState(context.Background(), audioState, true)

	sp.logger.Infow("adding audio track", "stereo", stereo, "codec", sp.codecParameters.MimeType)
	if err := sp.sdkOutput.AddAudioTrack(t, sp.codecParameters.MimeType, false, stereo); err != nil {
		return false, err
	}
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
	sampleProviders := []lksdk_output.VideoSampleProvider{}

	for _, track := range sp.tracks {
		if track.width != 0 && track.height != 0 {
			layers = append(layers, &livekit.VideoLayer{
				Width:   uint32(track.width),
				Height:  uint32(track.height),
				Quality: track.quality,
			})
			sampleProviders = append(sampleProviders, track)
		}
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

	if err := sp.sdkOutput.AddVideoTrack(sampleProviders, layers, sp.codecParameters.MimeType); err != nil {
		return false, err
	}

	for _, l := range layers {
		sp.logger.Infow("adding video track", "width", l.Width, "height", l.Height, "codec", sp.codecParameters.MimeType)
	}
	sp.sinkInitialized = true

	return sp.sinkInitialized, nil

}

func (sp *SDKMediaSink) ensureTracksInitialized(s *media.Sample, t *SDKMediaSinkTrack) (bool, error) {
	sp.tracksLock.Lock()
	defer sp.tracksLock.Unlock()

	if sp.sinkInitialized {
		return sp.sinkInitialized, nil
	}

	if sp.streamKind == types.Audio {
		return sp.ensureAudioTracksInitialized(s, t)
	}

	return sp.ensureVideoTracksInitialized(s, t)
}

func (t *SDKMediaSinkTrack) NextSample(ctx context.Context) (media.Sample, error) {
	for {
		select {
		case <-t.sink.fuse.Watch():
		case <-ctx.Done():
			return media.Sample{}, io.EOF
		case s := <-t.readySamples:
			g := t.sink.trackStatsGatherer.Load()
			if g != nil {
				g.MediaReceived(int64(len(s.s.Data)))
			}

			return *s.s, nil
		}
	}
}

func (t *SDKMediaSinkTrack) PushSample(s *media.Sample, ts time.Duration) error {
	if t.sink.fuse.IsBroken() {
		return io.EOF
	}

	tracksInitialized, err := t.sink.ensureTracksInitialized(s, t)
	if err != nil {
		return err
	} else if !tracksInitialized {
		// Drop the sample
		return nil
	}

	// Synchronize the outputs before the network jitter buffer to avoid old samples stuck
	// in the channel from increasing the whole pipeline delay.
	drop, err := t.sink.outputSync.WaitForMediaTime(ts)
	if err != nil {
		return err
	}
	if drop {
		t.sink.logger.Debugw("dropping sample", "timestamp", ts)
		return nil
	}

	select {
	case <-t.sink.fuse.Watch():
		return io.EOF
	case t.readySamples <- &sample{s, ts}:
	default:
		// drop the sample if the output queue is full. This is needed if we are reconnecting.
	}

	return nil
}

func (t *SDKMediaSinkTrack) Close() error {
	return t.sink.Close()
}

func (t *SDKMediaSinkTrack) OnBind() error {
	t.sink.logger.Infow("media sink bound")
	return nil
}

func (t *SDKMediaSinkTrack) OnUnbind() error {
	t.sink.logger.Infow("media sink unbound")
	return nil
}

func (t *SDKMediaSinkTrack) ForceKeyFrame() error {
	if t.writePLI != nil {
		t.writePLI()
	}

	return nil
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
