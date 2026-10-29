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

// Package gstutiltest builds and inspects GStreamer objects in C for tests, which cannot use cgo directly.
package gstutiltest

// #cgo pkg-config: gstreamer-1.0 gstreamer-mpegts-1.0
// #cgo CFLAGS: -DGST_USE_UNSTABLE_API
// #include <gst/gst.h>
// #include <gst/mpegts/mpegts.h>
//
// static GstEvent *scte_event(GstMpegtsSCTESIT *sit) {
//   GstMpegtsSection *s = gst_mpegts_section_from_scte_sit(sit, 500);
//   GstEvent *ev = gst_event_new_mpegts_section(s);
//   gst_mpegts_section_unref(s);
//   return ev;
// }
//
// static gint section_refcount(GstEvent *ev) {
//   const GstStructure *st = gst_event_get_structure(ev);
//   const GValue *v = st ? gst_structure_get_value(st, "section") : NULL;
//   return v ? GST_MINI_OBJECT_REFCOUNT_VALUE(g_value_get_boxed(v)) : -1;
// }
//
// static gint probe_event_refcount(GstPadProbeInfo *info) {
//   GstEvent *ev = GST_PAD_PROBE_INFO_EVENT(info);
//   return ev ? GST_MINI_OBJECT_REFCOUNT_VALUE(ev) : -1;
// }
import "C"

import (
	"runtime"
	"unsafe"

	"github.com/go-gst/go-gst/pkg/gst"
)

func NewSpliceOutEvent(eventID uint32, spliceTime, duration gst.ClockTime) *gst.Event {
	sit := C.gst_mpegts_scte_splice_out_new(C.guint32(eventID), C.GstClockTime(spliceTime), C.GstClockTime(duration))
	return gst.UnsafeEventFromGlibFull(unsafe.Pointer(C.scte_event(sit)))
}

func NewSpliceInEvent(eventID uint32, spliceTime gst.ClockTime) *gst.Event {
	sit := C.gst_mpegts_scte_splice_in_new(C.guint32(eventID), C.GstClockTime(spliceTime))
	return gst.UnsafeEventFromGlibFull(unsafe.Pointer(C.scte_event(sit)))
}

func NewNullSpliceEvent() *gst.Event {
	return gst.UnsafeEventFromGlibFull(unsafe.Pointer(C.scte_event(C.gst_mpegts_scte_null_new())))
}

// SectionRefCount returns the reference count of the section an event carries, or -1 when it has none.
func SectionRefCount(event *gst.Event) int {
	n := C.section_refcount((*C.GstEvent)(gst.UnsafeEventToGlibNone(event)))
	runtime.KeepAlive(event)
	return int(n)
}

// ProbeEventRefCount returns the reference count of the event a pad probe is called with,
// or -1 when the probe has no event.
func ProbeEventRefCount(info *gst.PadProbeInfo) int {
	return int(C.probe_event_refcount((*C.GstPadProbeInfo)(gst.UnsafePadProbeInfoToGlibNone(info))))
}
