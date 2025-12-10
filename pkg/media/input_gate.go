package media

import (
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
	"go.uber.org/atomic"
)

const (
	steadyBufferArrivalThreshold = 300 * time.Millisecond
	streamSteadyRatioThreshold   = 1.5
	streamSteadyWindow           = 100 * time.Millisecond
	streamSteadyRequiredWindows  = 2
	gateTerminatorTimeout        = 3 * time.Second
)

type padTimingState struct {
	lastBufferWallClockTime time.Time
	lastBufferPTS           time.Duration
	lastSteadyBuffArrival   time.Time

	firstBufferPTS           time.Duration
	firstBufferWallClockTime time.Time

	localOffset atomic.Duration

	gateCompleted atomic.Bool
	padOffset     atomic.Int64
	offsetReady   atomic.Bool
	skipOffset    atomic.Bool

	windowStreamAccum  time.Duration
	windowElapsedAccum time.Duration
	steadyWindowCount  int

	segmentStart atomic.Duration
}

func (i *Input) addGateProbe(pad *gst.Pad, padName string, state *padTimingState) {
	pad.AddProbe(gst.PadProbeTypeBuffer, func(pad *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		ok, pts, duration := extractBufferTiming(info, state)
		if !ok {
			return gst.PadProbeDrop
		}

		buffer := info.GetBuffer()

		if state.gateCompleted.Load() {
			if state.skipOffset.Load() {
				return gst.PadProbeOK
			}
			if !state.offsetReady.Load() {
				return gst.PadProbeDrop
			}
			applied := applyPadOffset(buffer, state, pts)
			if !applied {
				return gst.PadProbeDrop
			}
			return gst.PadProbeOK
		}

		wallClockTime := time.Now()

		if state.firstBufferPTS == 0 {
			state.firstBufferPTS = pts
			state.firstBufferWallClockTime = wallClockTime
		}

		if pts < state.lastBufferPTS {
			logger.Debugw("pts is less than last buffer pts", "pts", pts, "lastBufferPTS", state.lastBufferPTS, "pad", padName)
			return gst.PadProbeDrop
		}

		lastPTS := state.lastBufferPTS
		lastWallClockTime := state.lastBufferWallClockTime

		state.lastBufferPTS = pts
		state.lastBufferWallClockTime = wallClockTime
		state.localOffset.Store(pts + duration)

		if lastPTS == 0 {
			logger.Debugw("first buffer returning", "pts", pts, "pad", padName)
			state.lastSteadyBuffArrival = wallClockTime
			return gst.PadProbeDrop
		}

		streamTime := pts - lastPTS
		elapsedTime := wallClockTime.Sub(lastWallClockTime)

		if !streamSteady(wallClockTime, elapsedTime, streamTime, state) {
			return gst.PadProbeDrop
		}

		if wallClockTime.Sub(state.lastSteadyBuffArrival) > steadyBufferArrivalThreshold {
			logger.Debugw("arrival stable, allowing buffers to pass the probe", "pts", pts, "pad", padName)
			state.gateCompleted.Store(true)
			i.onPadGateReady(padName, state)
		}
		return gst.PadProbeDrop
	})
}

func (i *Input) addSegmentEventProbe(pad *gst.Pad, padName string, state *padTimingState) {
	pad.AddProbe(gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		event := info.GetEvent()
		if event == nil || event.Type() != gst.EventTypeSegment {
			return gst.PadProbeOK
		}

		seg := event.ParseSegment()
		if seg == nil {
			logger.Debugw("segment event missing data", "pad", padName)
			return gst.PadProbeOK
		}

		state.segmentStart.Store(time.Duration(seg.GetStart()))
		logger.Debugw("segment event received",
			"pad", padName,
			"format", seg.GetFormat(),
			"start", seg.GetStart(),
			"startDur", time.Duration(seg.GetStart()),
			"time", seg.GetTime(),
			"timeDur", time.Duration(seg.GetTime()),
			"rate", seg.GetRate(),
		)
		return gst.PadProbeOK
	})
}

func (i *Input) registerGatePad(padName string, state *padTimingState) {
	i.gateMu.Lock()
	defer i.gateMu.Unlock()

	// Gate terminator ensures we don't wait indefinitely for all pads to reach steady state.
	// This timeout prevents hanging when some streams have irregular buffer arrivals or never stabilize.
	// After 3 seconds, we stop waiting and let buffers flow without applying any offset to avoid
	// pushing a potentially incorrect shared offset.
	i.gateTerminator.Do(func() {
		go func() {
			time.Sleep(gateTerminatorTimeout)
			logger.Debugw("input ghost pad probe gate terminator triggered", "pad", padName)

			i.gateMu.Lock()
			defer i.gateMu.Unlock()
			if i.gateAllReady {
				return
			}
			logger.Warnw("gate terminator fired before all pads stabilized; skipping offset application", nil, "pad", padName)
			i.gateAllReady = true
			i.gateOffset = 0
			for _, st := range i.padTiming {
				st.skipOffset.Store(true)
				st.padOffset.Store(0)
				st.gateCompleted.Store(true)
				st.offsetReady.Store(true)
			}
			logger.Debugw("input ghost pad probe gate terminator completed", "pad", padName, "offset", i.gateOffset)
		}()
	})

	i.padTiming[padName] = state
	i.gateReady[padName] = false
	state.gateCompleted.Store(false)
	state.padOffset.Store(0)
	state.localOffset.Store(0)
	state.offsetReady.Store(false)
	state.skipOffset.Store(false)
}

func (i *Input) onPadGateReady(padName string, state *padTimingState) {
	i.gateMu.Lock()
	defer i.gateMu.Unlock()
	offset := state.localOffset.Load()

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
	for name := range i.padTiming {
		if !i.gateReady[name] {
			allReady = false
			break
		}
	}

	if !allReady {
		return
	}

	i.gateAllReady = true
	i.gateOffset = i.calculateMaxGatePadOffsetLocked()
	for _, st := range i.padTiming {
		st.padOffset.Store(int64(i.gateOffset))
		st.offsetReady.Store(true)
	}
}

func (i *Input) calculateMaxGatePadOffsetLocked() time.Duration {
	maxOffset := time.Duration(0)
	for _, st := range i.padTiming {
		localOffset := st.localOffset.Load()
		if localOffset > maxOffset {
			maxOffset = localOffset
		}
	}
	return maxOffset
}

func extractBufferTiming(info *gst.PadProbeInfo, state *padTimingState) (ok bool, pts time.Duration, duration time.Duration) {
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
		if state.lastBufferPTS == 0 {
			return true, *bufPTS, 0
		}
		duration = *bufPTS - state.lastBufferPTS
		bufDuration = &duration
	}

	return true, *bufPTS, *bufDuration
}

func streamSteady(now time.Time, elapsed, streamTime time.Duration, state *padTimingState) bool {
	state.windowStreamAccum += streamTime
	state.windowElapsedAccum += elapsed

	if state.windowElapsedAccum < streamSteadyWindow {
		return false
	}

	ratio := float64(state.windowStreamAccum) / float64(state.windowElapsedAccum)
	state.windowStreamAccum = 0
	state.windowElapsedAccum = 0

	if ratio <= streamSteadyRatioThreshold {
		state.steadyWindowCount++
		return state.steadyWindowCount >= streamSteadyRequiredWindows
	}
	state.steadyWindowCount = 0
	state.lastSteadyBuffArrival = now
	return false
}

func applyPadOffset(buffer *gst.Buffer, state *padTimingState, pts time.Duration) bool {
	offset := time.Duration(state.padOffset.Load())
	adj := pts - offset
	if adj < 0 {
		logger.Debugw("pts is smaller than pad offset, dropping packet", "pts", pts, "offset", offset)
		return false
	}

	// by design, every buffer must be preceded by SEGMENT (and SEGMENT is sticky),
	// so downstream elements will push STREAM_START → CAPS → SEGMENT before any buffers.
	// By the time buffers are processed, the segment start time will be set.
	adj += state.segmentStart.Load()

	buffer.SetPresentationTimestamp(gst.ClockTime(adj))
	return true
}
