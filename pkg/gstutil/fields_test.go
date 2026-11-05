// Copyright 2026 LiveKit, Inc.
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

package gstutil_test

import (
	"bytes"
	"log"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstmpegts"
	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/gstutil"
	"github.com/livekit/ingress/pkg/gstutil/gstutiltest"
)

func init() {
	gst.Init()
	gstmpegts.Initialize()
}

// captureLog returns what fn writes to the standard logger.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	fn()
	return buf.String()
}

// Both run on the buffer path, so they must not log.
func TestReferenceTimestamp(t *testing.T) {
	reference := gst.CapsFromString("application/x-livekit-latency")
	buffer := gst.NewBuffer()

	var ts gst.ClockTime
	var ok bool
	logged := captureLog(t, func() {
		gstutil.AddReferenceTimestamp(buffer, reference, gst.ClockTime(1234))
		ts, ok = gstutil.ReferenceTimestamp(buffer, reference)
	})

	require.True(t, ok)
	require.Equal(t, gst.ClockTime(1234), ts)
	require.Empty(t, logged)
}

func TestReferenceTimestampOtherReference(t *testing.T) {
	buffer := gst.NewBuffer()
	gstutil.AddReferenceTimestamp(buffer, gst.CapsFromString("timestamp/x-ntp"), gst.ClockTime(1234))

	_, ok := gstutil.ReferenceTimestamp(buffer, gst.CapsFromString("application/x-livekit-latency"))
	require.False(t, ok)
}

func TestParseSpliceInsertOut(t *testing.T) {
	ev := gstutiltest.NewSpliceOutEvent(7, gst.ClockTime(5*time.Second), gst.ClockTime(30*time.Second))

	var insert *gstutil.SpliceInsert
	logged := captureLog(t, func() {
		insert = gstutil.ParseSpliceInsert(ev)
	})

	require.NotNil(t, insert)
	require.Equal(t, []gstutil.SpliceEvent{{
		EventID:               7,
		OutOfNetworkIndicator: true,
	}}, insert.Splices)
	require.Empty(t, logged)
}

func TestParseSpliceInsertImmediateIn(t *testing.T) {
	insert := gstutil.ParseSpliceInsert(gstutiltest.NewSpliceInEvent(8, gst.ClockTimeNone))

	require.NotNil(t, insert)
	require.Equal(t, []gstutil.SpliceEvent{{
		EventID:             8,
		SpliceImmediateFlag: true,
	}}, insert.Splices)
}

func TestParseSpliceInsertIgnoresOtherCommands(t *testing.T) {
	require.Nil(t, gstutil.ParseSpliceInsert(gstutiltest.NewNullSpliceEvent()))
}

// The event's section must be left with the reference the event holds.
func TestParseSpliceInsertReleasesSection(t *testing.T) {
	for name, ev := range map[string]*gst.Event{
		"insert": gstutiltest.NewSpliceOutEvent(9, gst.ClockTime(time.Second), gst.ClockTime(10*time.Second)),
		"null":   gstutiltest.NewNullSpliceEvent(),
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, 1, gstutiltest.SectionRefCount(ev))
			gstutil.ParseSpliceInsert(ev)
			require.Equal(t, 1, gstutiltest.SectionRefCount(ev))
		})
	}
}

func TestParseSpliceInsertIgnoresOtherEvents(t *testing.T) {
	require.Nil(t, gstutil.ParseSpliceInsert(gst.NewEventEOS()))
}

func TestProbeWritableBuffer(t *testing.T) {
	for name, shared := range map[string]bool{"shared": true, "unshared": false} {
		t.Run(name, func(t *testing.T) {
			pad := gst.NewPad("src", gst.PadSrc)
			require.True(t, pad.SetActive(true))
			pad.PushEvent(gst.NewEventStreamStart("test"))
			segment := gst.NewSegment()
			segment.Init(gst.FormatTime)
			pad.PushEvent(gst.NewEventSegment(segment))

			copied := false
			var downstream gst.ClockTime
			pad.AddProbe(gst.PadProbeTypeBuffer, func(_ gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
				before := gst.UnsafeBufferToGlibNone(gstutil.ProbeBuffer(info))
				buffer := gstutil.ProbeWritableBuffer(info)
				copied = gst.UnsafeBufferToGlibNone(buffer) != before
				buffer.SetPTS(2)
				return gst.PadProbeOK
			})
			pad.AddProbe(gst.PadProbeTypeBuffer, func(_ gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
				downstream = gstutil.ProbeBuffer(info).PTS()
				return gst.PadProbeOK
			})

			held := gst.NewBuffer()
			held.SetPTS(1)
			pushed := held
			if shared {
				pushed = gst.UnsafeBufferFromGlibNone(gst.UnsafeBufferToGlibNone(held))
			}
			pad.Push(pushed)

			require.Equal(t, gst.ClockTime(2), downstream, "the modified buffer did not replace the probe data")
			require.Equal(t, shared, copied)
			if shared {
				require.Equal(t, gst.ClockTime(1), held.PTS(), "a shared buffer was modified in place")
			}
		})
	}
}

// ProbeEvent must not add a reference: one held by the Go wrapper is only dropped by a finalizer.
func TestProbeEventBorrows(t *testing.T) {
	pad := gst.NewPad("src", gst.PadSrc)
	require.True(t, pad.SetActive(true))

	called, named := false, false
	before, after := -1, -1
	pad.AddProbe(gst.PadProbeTypeEventDownstream, func(_ gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		called = true
		before = gstutiltest.ProbeEventRefCount(info)
		ev := gstutil.ProbeEvent(info)
		named = ev != nil && ev.HasName("probe-event-test")
		after = gstutiltest.ProbeEventRefCount(info)
		return gst.PadProbeOK
	})

	pad.PushEvent(gst.NewEventCustom(gst.EventCustomDownstreamOob, gst.NewStructureEmpty("probe-event-test")))

	require.True(t, called, "probe was not called")
	require.True(t, named, "ProbeEvent did not return the pushed event")
	require.Equal(t, before, after, "ProbeEvent changed the event's reference count")
}
