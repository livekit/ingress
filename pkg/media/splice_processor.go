// Copyright 2025 LiveKit, Inc.
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
	"fmt"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"

	"github.com/livekit/ingress/pkg/gstutil"
	"github.com/livekit/ingress/pkg/lksdk_output"
	"github.com/livekit/ingress/pkg/utils"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

type Splice struct {
	Immediate             bool
	OutOfNetworkIndicator bool
	EventId               uint32
	EventCancelIndicator  bool
	RunningTime           time.Duration
}

type SpliceProcessor struct {
	outputSync *utils.OutputSynchronizer
	sdkOut     *lksdk_output.LKSDKOutput

	ctx    context.Context
	cancel context.CancelFunc
}

func NewSpliceProcessor(sdkOut *lksdk_output.LKSDKOutput, outputSync *utils.OutputSynchronizer) *SpliceProcessor {
	su := &SpliceProcessor{
		sdkOut:     sdkOut,
		outputSync: outputSync,
	}

	su.ctx, su.cancel = context.WithCancel(context.Background())

	return su
}

func (su *SpliceProcessor) Close() {
	su.cancel()
}

func (su *SpliceProcessor) ProcessSpliceEvent(ev *gst.Event) error {
	scteSit := gstutil.ParseSpliceInsert(ev)
	if scteSit == nil {
		return nil
	}

	rMap, ok := ev.GetStructure().GetValue("running-time-map").(*gst.Structure)
	if !ok || rMap == nil {
		return nil
	}

	splices := scteSit.Splices
	for _, sp := range splices {
		var rTime time.Duration

		if !sp.SpliceImmediateFlag {
			if rMap != nil {
				if val, ok := rMap.GetUint64(fmt.Sprintf("event-%d-splice-time", sp.EventID)); ok {
					rTime = time.Duration(val)
				}
			}
			if rTime == 0 && scteSit.SpliceTimeSpecified {
				if val, ok := rMap.GetUint64("splice-time"); ok {
					rTime = time.Duration(val)
				}
			}
		}

		sp := &Splice{
			Immediate:             sp.SpliceImmediateFlag,
			OutOfNetworkIndicator: sp.OutOfNetworkIndicator,
			EventId:               sp.EventID,
			EventCancelIndicator:  sp.EventCancelIndicator,
			RunningTime:           time.Duration(rTime),
		}

		err := su.processSplice(sp)
		if err != nil {
			return err
		}
	}

	return nil
}

func (su *SpliceProcessor) processSplice(sp *Splice) error {
	var attr string

	if sp.OutOfNetworkIndicator {
		attr = fmt.Sprintf("%d", sp.EventId)
	}

	logger.Debugw("spice event", "eventID", sp.EventId, "outOfNetworkIndicator", sp.OutOfNetworkIndicator, "immediate", sp.Immediate, "runningTime", sp.RunningTime)

	event := func() {
		su.sdkOut.UpdateLocalParticipantAttributes(map[string]string{livekit.AttrIngressOutOfNetworkEventID: attr})
	}

	if sp.Immediate {
		event()
	} else {
		err := su.outputSync.ScheduleEvent(su.ctx, sp.RunningTime, event)
		if err != nil {
			return err
		}
	}

	return nil
}
