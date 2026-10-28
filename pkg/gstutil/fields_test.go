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

package gstutil

import (
	"bytes"
	"log"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstmpegts"
	"github.com/stretchr/testify/require"
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
		AddReferenceTimestamp(buffer, reference, gst.ClockTime(1234))
		ts, ok = ReferenceTimestamp(buffer, reference)
	})

	require.True(t, ok)
	require.Equal(t, gst.ClockTime(1234), ts)
	require.Empty(t, logged)
}

func TestReferenceTimestampOtherReference(t *testing.T) {
	buffer := gst.NewBuffer()
	AddReferenceTimestamp(buffer, gst.CapsFromString("timestamp/x-ntp"), gst.ClockTime(1234))

	_, ok := ReferenceTimestamp(buffer, gst.CapsFromString("application/x-livekit-latency"))
	require.False(t, ok)
}

func TestParseSpliceInsertOut(t *testing.T) {
	ev := newSpliceOutEvent(7, gst.ClockTime(5*time.Second), gst.ClockTime(30*time.Second))

	var insert *SpliceInsert
	logged := captureLog(t, func() {
		insert = ParseSpliceInsert(ev)
	})

	require.NotNil(t, insert)
	require.Equal(t, []SpliceEvent{{
		EventID:               7,
		OutOfNetworkIndicator: true,
	}}, insert.Splices)
	require.Empty(t, logged)
}

func TestParseSpliceInsertImmediateIn(t *testing.T) {
	insert := ParseSpliceInsert(newSpliceInEvent(8, gst.ClockTimeNone))

	require.NotNil(t, insert)
	require.Equal(t, []SpliceEvent{{
		EventID:             8,
		SpliceImmediateFlag: true,
	}}, insert.Splices)
}

func TestParseSpliceInsertIgnoresOtherCommands(t *testing.T) {
	require.Nil(t, ParseSpliceInsert(newNullSpliceEvent()))
}

// The event's section must be left with the reference the event holds.
func TestParseSpliceInsertReleasesSection(t *testing.T) {
	for name, ev := range map[string]*gst.Event{
		"insert": newSpliceOutEvent(9, gst.ClockTime(time.Second), gst.ClockTime(10*time.Second)),
		"null":   newNullSpliceEvent(),
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, 1, sectionRefCount(ev))
			ParseSpliceInsert(ev)
			require.Equal(t, 1, sectionRefCount(ev))
		})
	}
}

func TestParseSpliceInsertIgnoresOtherEvents(t *testing.T) {
	require.Nil(t, ParseSpliceInsert(gst.NewEventEOS()))
}
