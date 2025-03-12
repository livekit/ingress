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

package utils

import (
	"context"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/psrpc"
)

const (
	leeway = 500 * time.Millisecond
)

type OutputSynchronizer struct {
	lock sync.Mutex

	zeroTime time.Time
}

type TrackOutputSynchronizer struct {
	os              *OutputSynchronizer
	closed          core.Fuse
	firstSampleSent bool
}

func NewOutputSynchronizer() *OutputSynchronizer {
	return &OutputSynchronizer{}
}

func (os *OutputSynchronizer) ScheduleEvent(ctx context.Context, pts time.Duration, event func()) error {
	os.lock.Lock()

	if os.zeroTime.IsZero() {
		os.lock.Unlock()
		return psrpc.NewErrorf(psrpc.OutOfRange, "trying to schedule an event before output synchtonizer timeline was set")
	}

	waitTime := computeWaitDuration(pts, os.zeroTime, time.Now())

	os.lock.Unlock()

	go func() {
		select {
		case <-time.After(waitTime):
			event()
		case <-ctx.Done():
			return
		}
	}()

	return nil
}

func (os *OutputSynchronizer) AddTrack() *TrackOutputSynchronizer {
	return newTrackOutputSynchronizer(os)
}

func (os *OutputSynchronizer) getWaitDuration(pts time.Duration, firstSampleSent bool) time.Duration {
	os.lock.Lock()
	defer os.lock.Unlock()

	now := time.Now()
	waitTime := computeWaitDuration(pts, os.zeroTime, now)

	if os.zeroTime.IsZero() || (waitTime < -leeway && firstSampleSent) {
		// Reset zeroTime if the earliest track is late
		logger.Debugw("resetting synchronizer zero time", "lateDuration", -waitTime)
		os.zeroTime = now.Add(-pts)
		waitTime = 0
	}

	return waitTime
}

func newTrackOutputSynchronizer(os *OutputSynchronizer) *TrackOutputSynchronizer {
	return &TrackOutputSynchronizer{
		os: os,
	}
}

func (ost *TrackOutputSynchronizer) Close() {
	ost.closed.Break()
}

func (ost *TrackOutputSynchronizer) WaitForMediaTime(pts time.Duration) (bool, error) {
	waitTime := ost.os.getWaitDuration(pts, ost.firstSampleSent)

	if waitTime < -leeway {
		return true, nil
	}

	ost.firstSampleSent = true

	select {
	case <-time.After(waitTime):
		return false, nil
	case <-ost.closed.Watch():
		return false, errors.ErrIngressClosing
	}
}

func computeWaitDuration(pts time.Duration, zeroTime time.Time, now time.Time) time.Duration {
	mediaTime := zeroTime.Add(pts)

	return mediaTime.Sub(now)
}
