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
	maxTargetWaitDuration        = 100 * time.Millisecond
	waitDurationAdjustmentAmount = 10 * time.Millisecond
)

type OutputSynchronizer struct {
	lock sync.Mutex

	zeroTime time.Time

	tracks []*TrackOutputSynchronizer
}

type TrackOutputSynchronizer struct {
	os               *OutputSynchronizer
	closed           core.Fuse
	firstSampleSent  atomic.Bool
	lastWaitDuration atomic.Int64
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

	os.lock.Lock()
	defer os.lock.Unlock()

	os.tracks = append(os.tracks, t)

	return t
}

func (os *OutputSynchronizer) getWaitDuration(pts time.Duration, firstSampleSent bool) time.Duration {
	os.lock.Lock()
	defer os.lock.Unlock()

	now := time.Now()
	waitDuration := computeWaitDuration(pts, os.zeroTime, now)

	if os.zeroTime.IsZero() || (waitDuration < -leeway && firstSampleSent) {
		// Reset zeroTime if the earliest track is late
		os.zeroTime = now.Add(-pts)
		waitDuration = 0
	}

	waitDuration = os.handleWaitDurationTarget(firstSampleSent, waitDuration)

	return waitDuration
}

// must be called with lock held
func (os *OutputSynchronizer) handleWaitDurationTarget(firstSampleSent bool, currentWaitDuration time.Duration) time.Duration {
	if !firstSampleSent {
		return currentWaitDuration
	}

	minWaitDuration := currentWaitDuration
	for _, t := range os.tracks {
		d := time.Duration(t.lastWaitDuration.Load())
		if d <= minWaitDuration {
			minWaitDuration = d
		}
	}

	if minWaitDuration > maxTargetWaitDuration {
		for _, t := range os.tracks {
			t.lastWaitDuration.Add(int64(-waitDurationAdjustmentAmount))
		}
	}

	os.zeroTime = os.zeroTime.Add(-waitDurationAdjustmentAmount)

	return currentWaitDuration - waitDurationAdjustmentAmount
}

func newTrackOutputSynchronizer(os *OutputSynchronizer) *TrackOutputSynchronizer {
	return &TrackOutputSynchronizer{
		os: os,
	}
}

func (ost *TrackOutputSynchronizer) Close() {
	ost.closed.Break()
}

func (ost *TrackOutputSynchronizer) getWaitDurationForMediaTime(pts time.Duration) (time.Duration, bool, error) {
	waitDuration := ost.os.getWaitDuration(pts, ost.firstSampleSent.Load())

	ost.lastWaitDuration.Store(int64(waitDuration))

	if waitDuration < -leeway {
		return 0, true, nil
	}

	waitDuration = max(waitDuration, 0)

	ost.firstSampleSent.Store(true)

	return waitDuration, false, nil
}

func (ost *TrackOutputSynchronizer) WaitForMediaTime(pts time.Duration) (bool, error) {
	waitDuration, drop, err := ost.getWaitDurationForMediaTime(pts)
	if err != nil || drop {
		return drop, err
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
