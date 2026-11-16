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
	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstmpegts"
)

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
	section := gstmpegts.EventParseMpegtsSection(event)
	if section == nil {
		return nil
	}
	// Drop the section's reference now instead of at the next GC. The SIT and splice
	// events borrow from it, so they must not be used after this returns.
	defer gstmpegts.UnsafeSectionUnref(section)

	if section.GetSectionType() != gstmpegts.SectionScteSit {
		return nil
	}

	sit := section.GetScteSit()
	if sit == nil || sit.GetSpliceCommandType() != gstmpegts.GstMtsScteSpliceCommandInsert {
		return nil
	}

	ret := &SpliceInsert{SpliceTimeSpecified: sit.GetSpliceTimeSpecified()}
	for _, e := range sit.GetSplices() {
		ret.Splices = append(ret.Splices, SpliceEvent{
			EventID:               e.GetSpliceEventID(),
			EventCancelIndicator:  e.GetSpliceEventCancelIndicator(),
			OutOfNetworkIndicator: e.GetOutOfNetworkIndicator(),
			SpliceImmediateFlag:   e.GetSpliceImmediateFlag(),
		})
	}
	return ret
}
