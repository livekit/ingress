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
	"sync/atomic"
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

const (
	steadyBufferArrivalThreshold = 300 * time.Millisecond
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

	padTiming map[string]*padTimingState

	gateMu       sync.Mutex
	gateReady    map[string]bool
	gateAllReady bool
	gateOffset   time.Duration
}

type OutputReadyFunc func(pad *gst.Pad, kind types.StreamKind)

type padTimingState struct {
	lastBufferWallClockTime time.Time
	lastBufferPTS           time.Duration
	lastBufferDuration      time.Duration
	lastSteadyBuffArrival   time.Time

	firstBufferPTS           time.Duration
	firstBufferWallClockTime time.Time

	fastSequenceCnt int

	localOffset time.Duration

	gateCompleted atomic.Bool
	padOffset     atomic.Int64
	offsetReady   atomic.Bool
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
		padTiming:          make(map[string]*padTimingState),
		gateReady:          make(map[string]bool),
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
		}
	} else if strings.HasPrefix(pad.GetName(), "video") {
		if i.videoOutput == nil {
			newPad = true
			kind = types.Video
			i.videoOutput = pad
			ghostPad = gst.NewGhostPad("video", pad)
			timingState = &padTimingState{}
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
		padName := pad.GetName()

		state := timingState
		i.registerGatePad(padName, state)
		i.addGateProbe(pad, padName, state)

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

func (i *Input) addGateProbe(pad *gst.Pad, padName string, state *padTimingState) {
	pad.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		ret := gst.PadProbeDrop
		ok, pts, duration := extractBufferTiming(info)
		if !ok {
			return ret
		}

		buffer := info.GetBuffer()

		if state.gateCompleted.Load() {
			if !state.offsetReady.Load() {
				return ret
			}
			applied := applyPadOffset(buffer, state, pts)
			if !applied {
				return ret
			}
			logger.Debugw("new pts", "pts", buffer.PresentationTimestamp().AsDuration())
			return gst.PadProbeOK
		}

		wallClockTime := time.Now()

		if state.firstBufferPTS == 0 {
			state.firstBufferPTS = pts
			state.firstBufferWallClockTime = wallClockTime
		}

		if pts < state.lastBufferPTS {
			logger.Debugw("ghost pad probe, pts is less than last buffer pts", "pts", pts, "lastBufferPTS", state.lastBufferPTS)
			return ret
		}

		lastPTS := state.lastBufferPTS
		lastWallClockTime := state.lastBufferWallClockTime

		state.lastBufferPTS = pts
		state.lastBufferDuration = duration
		state.lastBufferWallClockTime = wallClockTime

		if lastPTS == 0 {
			logger.Debugw("ghost pad probe, first buffer returning", "pts", pts)
			state.lastSteadyBuffArrival = wallClockTime
			return ret
		}

		streamTime := pts - lastPTS
		elapsedTime := wallClockTime.Sub(lastWallClockTime)

		logger.Debugw("ghost pad probe, buffer received", "pts", pts, "streamTime", streamTime, "elapsedTime", elapsedTime)

		ratio := float64(streamTime) / float64(elapsedTime)
		if ratio > 1.5 {
			logger.Debugw("ghost pad probe, arrival not steady, dropping packet", "pts", pts)
			// don't let a single high ratio prolong the gate completion
			state.fastSequenceCnt++
			if state.fastSequenceCnt > 3 {
				state.lastSteadyBuffArrival = wallClockTime
				state.fastSequenceCnt = 0
			}
			return ret
		} else {
			state.fastSequenceCnt = 0
		}

		if wallClockTime.Sub(state.lastSteadyBuffArrival) > steadyBufferArrivalThreshold {
			logger.Debugw("ghost pad probe, arrival stable, allowing buffers to pass the probe", "pts", pts)
			offset := pts
			offset += duration
			state.gateCompleted.Store(true)
			state.offsetReady.Store(false)
			i.onPadGateReady(padName, state, offset)
			return gst.PadProbeDrop
		}

		logger.Debugw("ghost pad probe, waiting for steady arrival", "totalPts", pts-state.firstBufferPTS, "totalTime", wallClockTime.Sub(state.firstBufferWallClockTime))

		return ret
	})
}

func (i *Input) registerGatePad(padName string, state *padTimingState) {
	i.gateMu.Lock()
	defer i.gateMu.Unlock()

	i.padTiming[padName] = state
	i.gateReady[padName] = false
	state.gateCompleted.Store(false)
	state.padOffset.Store(0)
	state.localOffset = 0
	state.offsetReady.Store(false)
}

func (i *Input) onPadGateReady(padName string, state *padTimingState, offset time.Duration) {
	i.gateMu.Lock()
	defer i.gateMu.Unlock()

	state.localOffset = offset
	i.gateReady[padName] = true

	if i.gateAllReady {
		if offset > i.gateOffset {
			// very unlikely that a pad which wasn't added get delayed even more than pads which were added earlier
			logger.Warnw(
				"late pad reported larger offset; keeping shared offset", nil,
				"pad", padName,
				"sharedOffset", i.gateOffset,
				"lateOffset", offset,
			)
		}
		state.padOffset.Store(int64(i.gateOffset))
		state.offsetReady.Store(true)
		return
	}

	allReady := true
	maxOffset := offset
	for name, st := range i.padTiming {
		if !i.gateReady[name] {
			allReady = false
		} else if st.localOffset > maxOffset {
			maxOffset = st.localOffset
		}
	}

	if !allReady {
		return
	}

	i.gateAllReady = true
	i.gateOffset = maxOffset
	for _, st := range i.padTiming {
		st.padOffset.Store(int64(maxOffset))
		st.offsetReady.Store(true)
	}
}

func extractBufferTiming(info *gst.PadProbeInfo) (ok bool, pts time.Duration, duration time.Duration) {
	if info == nil {
		return
	}
	if info.Type()&gst.PadProbeTypeBuffer == 0 {
		return
	}

	buffer := info.GetBuffer()
	if buffer == nil {
		return
	}

	bufPTS := buffer.PresentationTimestamp().AsDuration()
	if bufPTS == nil {
		return
	}

	bufDuration := buffer.Duration().AsDuration()
	if bufDuration == nil {
		return
	}

	return true, *bufPTS, *bufDuration
}

func applyPadOffset(buffer *gst.Buffer, state *padTimingState, pts time.Duration) bool {
	offset := time.Duration(state.padOffset.Load())
	adj := pts - offset
	if adj < 0 {
		logger.Debugw("ghost pad probe, pts is less than pad offset, dropping packet", "pts", pts, "offset", offset)
		return false
	}

	buffer.SetPresentationTimestamp(gst.ClockTime(adj))
	return true
}
