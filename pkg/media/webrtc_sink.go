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

package media

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/lksdk_output"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/ingress/pkg/utils"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	putils "github.com/livekit/protocol/utils"
)

const (
	targetMinQueueLength = 2
)

type WebRTCSink struct {
	params    *params.Params
	onFailure func()

	lock             sync.Mutex
	sdkReady         core.Fuse
	closed           core.Fuse
	errChan          chan error
	spliceProbeAdded bool

	sdkOut          *lksdk_output.LKSDKOutput
	outputSync      *utils.OutputSynchronizer
	spliceProcessor *SpliceProcessor
	statsGatherer   *stats.LocalMediaStatsGatherer
	eos             *eosDispatcher

	// logging
	tooSlowThrottle  core.Throttle
	tooSlowLogEvents atomic.Int32
}

func NewWebRTCSink(ctx context.Context, p *params.Params, onFailure func(), statsGatherer *stats.LocalMediaStatsGatherer, eos *eosDispatcher) (*WebRTCSink, error) {
	ctx, span := tracer.Start(ctx, "media.NewWebRTCSink")
	defer span.End()

	s := &WebRTCSink{
		params:          p,
		onFailure:       onFailure,
		errChan:         make(chan error),
		outputSync:      utils.NewOutputSynchronizer(),
		statsGatherer:   statsGatherer,
		eos:             eos,
		tooSlowThrottle: core.NewThrottle(5 * time.Second),
	}

	go func() {
		var err error

		defer func() {
			s.sdkReady.Break()
			if err != nil {
				select {
				case s.errChan <- err:
				default:
				}
				if s.onFailure != nil {
					s.onFailure()
				}
			}
		}()

		sdkOut, err := lksdk_output.NewLKSDKOutput(ctx, onFailure, p)
		if err != nil {
			return
		}

		s.lock.Lock()
		s.sdkOut = sdkOut
		s.spliceProcessor = NewSpliceProcessor(sdkOut, s.outputSync)
		s.lock.Unlock()
	}()

	return s, nil
}

func (s *WebRTCSink) addAudioTrack() (*Output, error) {
	output, err := NewAudioOutput(s.params.AudioEncodingOptions, s.outputSync.AddTrack(), s.isPlayingTooSlow, s.statsGatherer, s.eos)
	if err != nil {
		logger.Errorw("could not create output", err)
		return nil, err
	}

	go func() {
		var sdkOut *lksdk_output.LKSDKOutput
		var err error

		defer func() {
			if err != nil {
				select {
				case s.errChan <- err:
				default:
				}
				if s.onFailure != nil {
					s.onFailure()
				}
			}
		}()

		select {
		case <-s.closed.Watch():
		case <-s.sdkReady.Watch():
			s.lock.Lock()
			sdkOut = s.sdkOut
			s.lock.Unlock()
		}

		if sdkOut != nil {
			var track *lksdk_output.LocalTrack
			track, err = sdkOut.AddAudioTrack(putils.GetMimeTypeForAudioCodec(s.params.AudioEncodingOptions.AudioCodec), s.params.AudioEncodingOptions.DisableDtx, s.params.AudioEncodingOptions.Channels > 1)
			if err != nil {
				return
			}

			output.SinkReady(track)

			sdkOut.AddOutputs(output)
		}
	}()

	return output.Output, nil
}

func (s *WebRTCSink) isPlayingTooSlow() bool {
	s.lock.Lock()
	sdkOut := s.sdkOut
	s.lock.Unlock()

	if sdkOut == nil {
		return false
	}

	if !s.params.Live {
		// output back pressure sets the play rate for VOD
		return false
	}

	o := sdkOut.GetOutputs()
	minQueueLength := math.MaxInt
	for _, out := range o {
		minQueueLength = min(minQueueLength, out.QueueLength())
	}

	if minQueueLength > targetMinQueueLength {
		s.tooSlowLogEvents.Add(1)

		s.tooSlowThrottle(func() {
			logger.Debugw("playing too slow", "minQueueLength", minQueueLength, "eventCount", s.tooSlowLogEvents.Swap(0))
		})

		return true
	}

	return false
}

func (s *WebRTCSink) addVideoTrack(w, h int) ([]*Output, error) {
	outputs := make([]*Output, 0)
	sbArray := make([]lksdk_output.SampleProvider, 0)

	sortedLayers := filterAndSortLayersByQuality(s.params.VideoEncodingOptions.Layers, w, h)

	for _, layer := range sortedLayers {
		output, err := NewVideoOutput(s.params.VideoEncodingOptions.VideoCodec, layer, s.outputSync.AddTrack(), s.isPlayingTooSlow, s.statsGatherer, s.eos)
		if err != nil {
			return nil, err
		}

		outputs = append(outputs, output.Output)
		sbArray = append(sbArray, output)
	}

	go func() {
		var sdkOut *lksdk_output.LKSDKOutput
		var err error

		defer func() {
			if err != nil {
				select {
				case s.errChan <- err:
				default:
				}
				if s.onFailure != nil {
					s.onFailure()
				}
			}
		}()

		select {
		case <-s.closed.Watch():
		case <-s.sdkReady.Watch():
			s.lock.Lock()
			sdkOut = s.sdkOut
			s.lock.Unlock()
		}

		if sdkOut != nil {
			var tracks []*lksdk_output.LocalTrack
			var pliHandlers []*lksdk_output.RTCPHandler

			tracks, pliHandlers, err = sdkOut.AddVideoTrack(sortedLayers, putils.GetMimeTypeForVideoCodec(s.params.VideoEncodingOptions.VideoCodec))
			if err != nil {
				return
			}

			for i, o := range outputs {
				o.SinkReady(tracks[i])
				pliHandlers[i].SetKeyFrameEmitter(o)
			}

			sdkOut.AddOutputs(sbArray...)
		}

	}()

	return outputs, nil
}

func (s *WebRTCSink) AddTrack(kind types.StreamKind, caps *gst.Caps) (*gst.Bin, error) {
	var bin *gst.Bin

	switch kind {
	case types.Audio:
		output, err := s.addAudioTrack()
		if err != nil {
			logger.Errorw("could not add audio track", err)
			return nil, err
		}

		bin = output.bin

	case types.Video:
		w, h, err := getResolution(caps)
		if err != nil {
			return nil, err
		}

		logger.Infow("source resolution parsed", "width", w, "height", h)

		outputs, err := s.addVideoTrack(w, h)
		if err != nil {
			logger.Errorw("could not add video track", err)
			return nil, err
		}

		pp, err := NewVideoOutputBin(s.params.VideoEncodingOptions, outputs)
		if err != nil {
			logger.Errorw("could not create video output bin", err)
			return nil, err
		}

		bin = pp.GetBin()
	}

	if !s.spliceProbeAdded {
		s.addSpliceProbe(bin)
	}
	return bin, nil
}

func (s *WebRTCSink) Close() error {
	s.closed.Break()

	<-s.sdkReady.Watch()

	var err error
	s.lock.Lock()
	if s.spliceProcessor != nil {
		s.spliceProcessor.Close()
	}

	if s.sdkOut != nil {
		err = s.sdkOut.Close()
	}
	s.lock.Unlock()

	if err == nil {
		select {
		case err = <-s.errChan:
		default:
		}
	}

	return err
}

func getResolution(caps *gst.Caps) (w int, h int, err error) {
	if caps.GetSize() == 0 {
		return 0, 0, errors.ErrUnsupportedDecodeFormat
	}

	str := caps.GetStructureAt(0)

	wObj, err := str.GetValue("width")
	if err != nil {
		return 0, 0, err
	}

	hObj, err := str.GetValue("height")
	if err != nil {
		return 0, 0, err
	}

	return wObj.(int), hObj.(int), nil
}

func (s *WebRTCSink) addSpliceProbe(bin *gst.Bin) {
	pad := bin.GetStaticPad("sink")
	if pad == nil {
		logger.Infow("No sink pad on output bin")
		return
	}

	pad.SetEventFunction(func(self *gst.Pad, parent *gst.Object, event *gst.Event) bool {
		if event.HasName("scte-sit") {
			s.lock.Lock()
			p := s.spliceProcessor
			s.lock.Unlock()

			if p != nil {
				err := p.ProcessSpliceEvent(event)
				if err != nil {
					logger.Infow("failed processing splice event", "error", err)
				}
			} else {
				// TODO store events and process them after connection
				logger.Infow("unable to process media splice before room is connected")
			}
		}

		return pad.EventDefault(parent, event)
	})

	s.spliceProbeAdded = true
}

func filterAndSortLayersByQuality(layers []*livekit.VideoLayer, sourceW, sourceH int) []*livekit.VideoLayer {
	layersByQuality := make(map[livekit.VideoQuality]*livekit.VideoLayer)

	for _, layer := range layers {
		layersByQuality[layer.Quality] = layer
	}

	var ret []*livekit.VideoLayer
	for q := livekit.VideoQuality_LOW; q <= livekit.VideoQuality_HIGH; q++ {
		layer, ok := layersByQuality[q]
		if !ok {
			continue
		}

		applyResolutionToLayer(layer, sourceW, sourceH)

		ret = append(ret, layer)

		if layer.Width >= uint32(sourceW) && layer.Height >= uint32(sourceH) {
			// Next quality layer would be duplicate of current one
			break
		}

	}
	return ret
}

func applyResolutionToLayer(layer *livekit.VideoLayer, sourceW, sourceH int) {
	w := uint32(sourceW)
	h := uint32(sourceH)

	if w > layer.Width {
		w = layer.Width
		h = uint32((int64(w) * int64(sourceH)) / int64(sourceW))
	}

	if h > layer.Height {
		h = layer.Height
		w = uint32((int64(h) * int64(sourceW)) / int64(sourceH))
	}

	// Roubd up to the next even dimension
	w = ((w + 1) >> 1) << 1
	h = ((h + 1) >> 1) << 1

	layer.Width = w
	layer.Height = h
}
