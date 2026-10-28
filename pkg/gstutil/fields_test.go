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

func spliceEvent(sit *gstmpegts.SCTESIT) *gst.Event {
	return gstmpegts.EventNewMpegtsSection(gstmpegts.SectionFromScteSit(sit, 500))
}

func TestParseSpliceInsertOut(t *testing.T) {
	ev := spliceEvent(gstmpegts.NewScteSpliceOut(7, gst.ClockTime(5*time.Second), gst.ClockTime(30*time.Second)))

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
	insert := ParseSpliceInsert(spliceEvent(gstmpegts.NewScteSpliceIn(8, gst.ClockTimeNone)))

	require.NotNil(t, insert)
	require.Equal(t, []SpliceEvent{{
		EventID:             8,
		SpliceImmediateFlag: true,
	}}, insert.Splices)
}

func TestParseSpliceInsertIgnoresOtherCommands(t *testing.T) {
	require.Nil(t, ParseSpliceInsert(spliceEvent(gstmpegts.NewScteNull())))
}

func TestParseSpliceInsertIgnoresOtherEvents(t *testing.T) {
	require.Nil(t, ParseSpliceInsert(gst.NewEventEOS()))
}
