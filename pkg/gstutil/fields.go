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

// Struct fields the go-gst bindings do not expose.

// #cgo pkg-config: gstreamer-1.0 gstreamer-mpegts-1.0
// #cgo CFLAGS: -DGST_USE_UNSTABLE_API
// #include <gst/gst.h>
// #include <gst/mpegts/mpegts.h>
//
// static void section_unref(GstMpegtsSection *s) { gst_mpegts_section_unref(s); }
import "C"

import (
	"runtime"
	"time"
	"unsafe"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstmpegts"
)

// ReferenceTimestamp returns the timestamp of the buffer's reference timestamp meta
// for reference, or false when the buffer has none.
func ReferenceTimestamp(buffer *gst.Buffer, reference *gst.Caps) (gst.ClockTime, bool) {
	m := C.gst_buffer_get_reference_timestamp_meta(
		(*C.GstBuffer)(gst.UnsafeBufferToGlibNone(buffer)),
		(*C.GstCaps)(gst.UnsafeCapsToGlibNone(reference)),
	)
	if m == nil {
		return 0, false
	}
	ts := gst.ClockTime(m.timestamp)
	runtime.KeepAlive(buffer)
	runtime.KeepAlive(reference)
	return ts, true
}

// AddReferenceTimestamp attaches a reference timestamp meta for reference to the buffer.
func AddReferenceTimestamp(buffer *gst.Buffer, reference *gst.Caps, timestamp gst.ClockTime) {
	C.gst_buffer_add_reference_timestamp_meta(
		(*C.GstBuffer)(gst.UnsafeBufferToGlibNone(buffer)),
		(*C.GstCaps)(gst.UnsafeCapsToGlibNone(reference)),
		C.GstClockTime(timestamp),
		0,
	)
	runtime.KeepAlive(buffer)
	runtime.KeepAlive(reference)
}

type SpliceInsert struct {
	SpliceTimeSpecified bool
	Splices             []SpliceEvent
}

type SpliceEvent struct {
	EventID               uint32
	EventCancelIndicator  bool
	OutOfNetworkIndicator bool
	SpliceImmediateFlag   bool
}

// ParseSpliceInsert returns the splice_insert command carried by an SCTE-35 section
// event, or nil for any other event or command.
func ParseSpliceInsert(event *gst.Event) *SpliceInsert {
	// The generated EventParseMpegtsSection frees the returned reference with C.free.
	s := C.gst_event_parse_mpegts_section((*C.GstEvent)(gst.UnsafeEventToGlibNone(event)))
	runtime.KeepAlive(event)
	if s == nil {
		return nil
	}
	defer C.section_unref(s)

	if gstmpegts.SectionType(s.section_type) != gstmpegts.SectionScteSit {
		return nil
	}

	c := C.gst_mpegts_section_get_scte_sit(s)
	if c == nil {
		return nil
	}
	if gstmpegts.SCTESpliceCommandType(c.splice_command_type) != gstmpegts.GstMtsScteSpliceCommandInsert {
		return nil
	}

	ret := &SpliceInsert{SpliceTimeSpecified: c.splice_time_specified != 0}
	if c.splices == nil {
		return ret
	}
	events := unsafe.Slice((**C.GstMpegtsSCTESpliceEvent)(unsafe.Pointer(c.splices.pdata)), c.splices.len)
	for _, e := range events {
		ret.Splices = append(ret.Splices, SpliceEvent{
			EventID:               uint32(e.splice_event_id),
			EventCancelIndicator:  e.splice_event_cancel_indicator != 0,
			OutOfNetworkIndicator: e.out_of_network_indicator != 0,
			SpliceImmediateFlag:   e.splice_immediate_flag != 0,
		})
	}
	return ret
}

// DurationOf converts a clock time to a duration, or false for GST_CLOCK_TIME_NONE.
func DurationOf(t gst.ClockTime) (time.Duration, bool) {
	if t == gst.ClockTimeNone {
		return 0, false
	}
	return time.Duration(t), true
}

// ProbeBuffer returns the buffer a pad probe is called with, without taking a reference
// on it, or nil when the probe is not for a buffer. The buffer is only valid until the
// probe returns.
func ProbeBuffer(info *gst.PadProbeInfo) *gst.Buffer {
	i := (*C.GstPadProbeInfo)(gst.UnsafePadProbeInfoToGlibNone(info))
	if i._type&C.GST_PAD_PROBE_TYPE_BUFFER == 0 {
		return nil
	}
	b := C.gst_pad_probe_info_get_buffer(i)
	if b == nil {
		return nil
	}
	return gst.UnsafeBufferFromGlibBorrow(unsafe.Pointer(b))
}

func BufferIsWritable(buffer *gst.Buffer) bool {
	return gst.UnsafeMiniObjectFromGlibBorrow(gst.UnsafeBufferToGlibNone(buffer)).IsWritable()
}
