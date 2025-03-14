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
	"sync/atomic"
	"time"

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/psrpc"
)

const (
	leeway                       = 100 * time.Millisecond
	waitDurationAdjustmentAmount = 10 * time.Millisecond
)

type OutputSynchronizer struct {
	lock sync.Mutex

	zeroTime time.Time
}

type TrackOutputSynchronizer struct {
	os              *OutputSynchronizer
	closed          core.Fuse
	firstSampleSent atomic.Bool
}

func NewOutputSynchronizer() *OutputSynchronizer {
	return &OutputSynchronizer{}
}

// TODO adjust timer deadline when zeroTime is adjusted
func (os *OutputSynchronizer) ScheduleEvent(ctx context.Context, pts time.Duration, event func()) error {
	os.lock.Lock()

	if os.zeroTime.IsZero() {
		os.lock.Unlock()
		return psrpc.NewErrorf(psrpc.OutOfRange, "trying to schedule an event before output synchtonizer timeline was set")
	}

	waitDuration := computeWaitDuration(pts, os.zeroTime, time.Now())

	os.lock.Unlock()

	go func() {
		select {
		case <-time.After(waitDuration):
			event()
		case <-ctx.Done():
			return
		}
	}()

	return nil
}

func (os *OutputSynchronizer) AddTrack() *TrackOutputSynchronizer {
	t := newTrackOutputSynchronizer(os)

	return t
}

func (os *OutputSynchronizer) getWaitDuration(pts time.Duration, firstSampleSent bool, zeroTimeAdjustment time.Duration) time.Duration {
	os.lock.Lock()
	defer os.lock.Unlock()

	if !os.zeroTime.IsZero() && firstSampleSent {
		os.zeroTime = os.zeroTime.Add(zeroTimeAdjustment)
	}

	now := time.Now()
	waitDuration := computeWaitDuration(pts, os.zeroTime, now)

	if os.zeroTime.IsZero() || (waitDuration < -leeway && firstSampleSent) {
		// Reset zeroTime if the earliest track is late
		os.zeroTime = now.Add(-pts)
		waitDuration = 0
	}

	return waitDuration
}

func newTrackOutputSynchronizer(os *OutputSynchronizer) *TrackOutputSynchronizer {
	return &TrackOutputSynchronizer{
		os: os,
	}
}

func (ost *TrackOutputSynchronizer) Close() {
	ost.closed.Break()
}

func (ost *TrackOutputSynchronizer) getWaitDurationForMediaTime(pts time.Duration, playingTooSlow bool) (time.Duration, bool) {
	zeroTimeAdjustment := time.Duration(0)

	if playingTooSlow {
		zeroTimeAdjustment = -waitDurationAdjustmentAmount
	}

	waitDuration := ost.os.getWaitDuration(pts, ost.firstSampleSent.Load(), zeroTimeAdjustment)

	if waitDuration < -leeway {
		return 0, true
	}

	waitDuration = max(waitDuration, 0)

	ost.firstSampleSent.Store(true)

	return waitDuration, false
}

func (ost *TrackOutputSynchronizer) WaitForMediaTime(pts time.Duration, playingTooSlow bool) (bool, error) {
	waitDuration, drop := ost.getWaitDurationForMediaTime(pts, playingTooSlow)
	if drop {
		return drop, nil
	}

	select {
	case <-time.After(waitDuration):
		return false, nil
	case <-ost.closed.Watch():
		return false, errors.ErrIngressClosing
	}
}

func computeWaitDuration(pts time.Duration, zeroTime time.Time, now time.Time) time.Duration {
	mediaTime := zeroTime.Add(pts)

	return mediaTime.Sub(now)
}
