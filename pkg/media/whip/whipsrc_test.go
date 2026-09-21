// Copyright 2024 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package whip

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newOffsetSource() *WHIPSource {
	s := &WHIPSource{}
	s.startOffset.Store(-1)
	return s
}

// The offset is latched by whichever track delivers its first packet first, and
// the tracks are independent streams, so the other one can carry an earlier
// timestamp. The corrected value feeds gst.ClockTime, which is unsigned, so a
// negative duration would arrive downstream as a value near 2^64.
func TestCorrectedTimestampIsNeverNegative(t *testing.T) {
	s := newOffsetSource()

	require.Zero(t, s.getCorrectedTimestamp(117835*time.Nanosecond),
		"the track that latches the offset starts at zero")

	got := s.getCorrectedTimestamp(1 * time.Nanosecond)

	require.GreaterOrEqual(t, got, time.Duration(0),
		"a timestamp earlier than the latched offset must not correct to a negative")
	require.Less(t, uint64(got), uint64(1)<<63,
		"a negative duration reaches the pipeline as a huge unsigned GstClockTime")
}

// Clamping only applies to packets that predate the offset. Everything after it
// keeps its spacing, which is what the shared offset exists to preserve.
func TestCorrectedTimestampKeepsSpacingAfterTheOffset(t *testing.T) {
	s := newOffsetSource()

	s.getCorrectedTimestamp(117835 * time.Nanosecond)

	require.Equal(t, 33215499*time.Nanosecond,
		s.getCorrectedTimestamp(33333334*time.Nanosecond),
		"a later packet keeps its distance from the latched offset")
}

// Both tracks correct against the same offset, so the one that latched it is
// unaffected by the other arriving earlier.
func TestLatchingTrackIsUnaffectedByAnEarlierPeer(t *testing.T) {
	s := newOffsetSource()

	s.getCorrectedTimestamp(117835 * time.Nanosecond)
	s.getCorrectedTimestamp(1 * time.Nanosecond)

	require.Equal(t, 15000000*time.Nanosecond,
		s.getCorrectedTimestamp(15117835*time.Nanosecond),
		"the latched offset must not move once set")
}

// A large gap between the tracks clamps a run of packets, not just one: every
// packet older than the offset reads zero, and spacing resumes past it.
func TestCorrectedTimestampClampsEveryPacketOlderThanTheOffset(t *testing.T) {
	s := newOffsetSource()

	s.getCorrectedTimestamp(200 * time.Millisecond)

	for _, ts := range []time.Duration{
		1 * time.Nanosecond,
		50 * time.Millisecond,
		100 * time.Millisecond,
		199 * time.Millisecond,
	} {
		require.Zero(t, s.getCorrectedTimestamp(ts),
			"a packet older than the offset clamps to zero, however many there are")
	}

	require.Equal(t, 50*time.Millisecond,
		s.getCorrectedTimestamp(250*time.Millisecond),
		"the first packet past the offset resumes normal spacing")
}
