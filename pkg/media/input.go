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
	"strings"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/media/rtmp"
	"github.com/livekit/ingress/pkg/media/urlpull"
	"github.com/livekit/ingress/pkg/media/whip"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/ingress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

type Source interface {
	GetSources() []*gst.Element
	ValidateCaps(*gst.Caps) error
	Start(ctx context.Context, onClose func()) error
	Close() error
}

type Input struct {
	lock sync.Mutex

	trackStatsGatherer map[types.StreamKind]*stats.MediaTrackStatGatherer

	bin    *gst.Bin
	source Source

	audioOutput *gst.Pad
	videoOutput *gst.Pad

	onOutputReady OutputReadyFunc
	closeFuse     core.Fuse
	closeErr      error

	padTiming map[types.StreamKind]*padTimingState
}

type OutputReadyFunc func(pad *gst.Pad, kind types.StreamKind)

type padTimingState struct {
	lastBufferWallClockTime time.Time
	lastBufferPTS           time.Duration
	lastBufferDuration      time.Duration
	lastSteadyBuffArrival   time.Time

	firstBufferPTS           time.Duration
	firstBufferWallClockTime time.Time

	gateCompleted bool
	padOffset     time.Duration
}

func NewInput(ctx context.Context, p *params.Params, g *stats.LocalMediaStatsGatherer) (*Input, error) {
	src, err := CreateSource(ctx, p)
	if err != nil {
		return nil, err
	}

	bin := gst.NewBin("input")
	i := &Input{
		bin:                bin,
		source:             src,
		trackStatsGatherer: make(map[types.StreamKind]*stats.MediaTrackStatGatherer),
		padTiming:          make(map[types.StreamKind]*padTimingState),
	}

	if p.InputType == livekit.IngressInput_URL_INPUT {
		// Gather input stats from the pipeline
		i.trackStatsGatherer[types.Audio] = g.RegisterTrackStats(stats.InputAudio)
		i.trackStatsGatherer[types.Video] = g.RegisterTrackStats(stats.InputVideo)
	}

	srcs := src.GetSources()
	if len(srcs) == 0 {
		return nil, errors.ErrSourceNotReady
	}

	for _, src := range srcs {
		decodeBin, err := gst.NewElement("decodebin3")
		if err != nil {
			return nil, err
		}

		if err := bin.AddMany(decodeBin, src); err != nil {
			return nil, err
		}

		if _, err = decodeBin.Connect("pad-added", i.onPadAdded); err != nil {
			return nil, err
		}

		if err = src.Link(decodeBin); err != nil {
			return nil, err
		}
	}

	return i, nil
}

func CreateSource(ctx context.Context, p *params.Params) (Source, error) {
	switch p.InputType {
	case livekit.IngressInput_RTMP_INPUT:
		return rtmp.NewRTMPRelaySource(ctx, p)
	case livekit.IngressInput_WHIP_INPUT:
		return whip.NewWHIPRelaySource(ctx, p)
	case livekit.IngressInput_URL_INPUT:
		return urlpull.NewURLSource(ctx, p)
	default:
		return nil, ingress.ErrInvalidIngressType
	}
}

func (i *Input) OnOutputReady(f OutputReadyFunc) {
	i.onOutputReady = f
}

func (i *Input) Start(ctx context.Context, onCloseTimeout func(ctx context.Context)) error {
	return i.source.Start(ctx, func() {
		go func() {
			t := time.NewTimer(5 * time.Second)
			select {
			case <-t.C:
				logger.Infow("timeout while waiting for source closure to trigger pipeline stop. Pipeline frozen")
				if onCloseTimeout != nil {
					onCloseTimeout(context.Background())
				}
			case <-i.closeFuse.Watch():
				t.Stop()
			}
		}()
	})
}

func (i *Input) Close() error {
	// Make sure Close is idempotent and always return the input error
	i.closeFuse.Once(func() {
		i.closeErr = i.source.Close()
	})

	return i.closeErr
}

func (i *Input) onPadAdded(_ *gst.Element, pad *gst.Pad) {
	var err error

	defer func() {
		if err != nil {
			msg := gst.NewErrorMessage(i.bin.Element, err, err.Error(), nil)
			i.bin.Element.GetBus().Post(msg)
		}
	}()

	typefind, err := i.bin.GetElementByName("typefind")
	if err == nil && typefind != nil {
		var caps interface{}
		caps, err = typefind.GetProperty("caps")
		if err == nil && caps != nil {
			err = i.source.ValidateCaps(caps.(*gst.Caps))
			if err != nil {
				logger.Infow("input caps validation failed", "error", err)
				return
			}
		}
	}

	// Make sure we emit scte35 markers if available
	tsparser, _ := i.bin.GetElementByName("tsdemux0")
	if tsparser != nil {
		err := tsparser.SetProperty("send-scte35-events", true)
		if err != nil {
			logger.Errorw("failed setting `send-scte35-events` property", err)
		}
	}

	// surface callback for first audio and video pads, plug in fakesink on the rest
	i.lock.Lock()
	newPad := false
	var kind types.StreamKind
	var ghostPad *gst.GhostPad
	var timingState *padTimingState
	if strings.HasPrefix(pad.GetName(), "audio") {
		if i.audioOutput == nil {
			newPad = true
			kind = types.Audio
			i.audioOutput = pad
			ghostPad = gst.NewGhostPad("audio", pad)
			timingState = &padTimingState{}
			i.padTiming[kind] = timingState
		}
	} else if strings.HasPrefix(pad.GetName(), "video") {
		if i.videoOutput == nil {
			newPad = true
			kind = types.Video
			i.videoOutput = pad
			ghostPad = gst.NewGhostPad("video", pad)
			timingState = &padTimingState{}
			i.padTiming[kind] = timingState
		}
	}
	i.lock.Unlock()

	// don't need this pad, link to fakesink
	if newPad {
		if !i.bin.AddPad(ghostPad.Pad) {
			logger.Errorw("failed to add ghost pad", nil)
			return
		}
		pad = ghostPad.Pad

		state := timingState
		pad.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			ret := gst.PadProbeDrop
			if state.gateCompleted {
				ret = gst.PadProbeOK
			}

			if info == nil {
				return ret
			}

			if info.Type()&gst.PadProbeTypeBuffer == 0 {
				if info.Type() == gst.PadProbeTypeBufferList {
					logger.Debugw("ghost pad probe, info type is buffer list")
					return ret
				}
				return ret
			}

			buffer := info.GetBuffer()
			if buffer == nil {
				logger.Debugw("ghost pad probe, buffer is nil")
				return ret
			}

			pts := buffer.PresentationTimestamp().AsDuration()
			if pts == nil {
				logger.Debugw("ghost pad probe, pts is nil")
				return ret
			}

			if state.gateCompleted {
				logger.Debugw("ghost pad probe, applying pad offset", "offset", state.padOffset)
				if *pts-state.padOffset < 0 {
					logger.Debugw("ghost pad probe, pts is less than pad offset", "pts", *pts, "offset", state.padOffset)
					return gst.PadProbeDrop
				}
				buffer.SetPresentationTimestamp(gst.ClockTime(*pts - state.padOffset))
				logger.Debugw("new pts", "pts", buffer.PresentationTimestamp().AsDuration())
				return gst.PadProbeOK
			}

			wallClockTime := time.Now()

			if state.firstBufferPTS == 0 {
				state.firstBufferPTS = *pts
				state.firstBufferWallClockTime = wallClockTime
			}

			if *pts < state.lastBufferPTS {
				logger.Debugw("ghost pad probe, pts is less than last buffer pts", "pts", *pts, "lastBufferPTS", state.lastBufferPTS)
				return ret
			}

			if state.lastBufferPTS == 0 {
				logger.Debugw("ghost pad probe, first buffer returning", "pts", *pts)
				state.lastBufferPTS = *pts
				if duration := buffer.Duration().AsDuration(); duration != nil {
					state.lastBufferDuration = *duration
				}
				state.lastBufferWallClockTime = wallClockTime
				state.lastSteadyBuffArrival = wallClockTime
				return ret
			}

			duration := buffer.Duration().AsDuration()

			streamTime := *pts - state.lastBufferPTS
			elapsedTime := wallClockTime.Sub(state.lastBufferWallClockTime)

			state.lastBufferPTS = *pts
			if duration != nil {
				state.lastBufferDuration = *duration
			}
			state.lastBufferWallClockTime = wallClockTime

			logger.Debugw("ghost pad probe, buffer received", "pts", *pts, "streamTime", streamTime, "elapsedTime", elapsedTime)

			if elapsedTime <= 0 {
				logger.Debugw("ghost pad probe, elapsed time non-positive", "elapsedTime", elapsedTime)
				state.lastSteadyBuffArrival = wallClockTime
				return ret
			}

			ratio := float64(streamTime) / float64(elapsedTime)
			if ratio > 1.5 {
				// still not steady
				state.lastSteadyBuffArrival = wallClockTime
				logger.Debugw("ghost pad probe, arrival not steady, dropping packet", "pts", *pts)
				return ret
			}

			if wallClockTime.Sub(state.lastSteadyBuffArrival) > 300*time.Millisecond {
				// steady buffer arrival, we are done
				logger.Debugw("ghost pad probe, arrival stable, removing the probe", "pts", *pts)
				state.gateCompleted = true
				state.padOffset = *pts + *duration
				return gst.PadProbeDrop
			}

			logger.Debugw("ghost pad probe, waiting for steady arrival", "totalPts", *pts-state.firstBufferPTS, "totalTime", wallClockTime.Sub(state.firstBufferWallClockTime))

			return ret
		})

		if i.trackStatsGatherer[kind] != nil {
			// Gather bitrate stats from pipeline itself
			i.addBitrateProbe(kind)
		}
	} else {
		var sink *gst.Element

		sink, err = gst.NewElement("fakesink")
		if err != nil {
			logger.Errorw("failed to create fakesink", err)
			return
		}
		var pads []*gst.Pad

		pads, err = sink.GetSinkPads()
		pad.Link(pads[0])
		return
	}

	if i.onOutputReady != nil {
		i.onOutputReady(pad, kind)
	}
}

func (i *Input) addBitrateProbe(kind types.StreamKind) {
	// Do a best effort to add probe to retrieve bitrate.
	// The multiqueue is generally created in the pipeline before the decoders
	mq, err := i.bin.GetElementByName("multiqueue0")

	if err != nil {
		// No multiqueue in that pipeline
		logger.Debugw("could not retrieve multiqueue element from pipeline", "error", err)
		return
	}

	pads, err := mq.GetSinkPads()
	if err != nil {
		logger.Errorw("failed retrieving multiqueue sink pads", err)
		return
	}

	for _, pad := range pads {
		caps := pad.GetCurrentCaps()
		if caps != nil && caps.GetSize() > 0 {
			gstStruct := caps.GetStructureAt(0)
			padKind := getKindFromGstMimeType(gstStruct)

			if padKind == kind {
				g := i.trackStatsGatherer[kind]

				pad.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
					buffer := info.GetBuffer()
					if buffer == nil {
						return gst.PadProbeOK
					}

					size := buffer.GetSize()
					g.MediaReceived(size)

					return gst.PadProbeOK
				})

				return
			}
		} else {
			logger.Debugw("could not retrieve multiqueue pad caps", "error", err)
		}
	}

	logger.Debugw("no pad on multiqueue with required kind found", "kind", kind)
}
