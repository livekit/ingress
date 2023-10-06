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
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/errors"
)

const (
	leeway = 100 * time.Millisecond
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

func (os *OutputSynchronizer) AddTrack() *TrackOutputSynchronizer {
	return newTrackOutputSynchronizer(os)
}

func (os *OutputSynchronizer) getWaitDuration(pts time.Duration, firstSampleSent bool) time.Duration {
	os.lock.Lock()
	defer os.lock.Unlock()

	mediaTime := os.zeroTime.Add(pts)
	now := time.Now()

	waitTime := mediaTime.Sub(now)

	if os.zeroTime.IsZero() || (waitTime < leeway && firstSampleSent) {
		// Reset zeroTime if the earliest track is late
		os.zeroTime = now.Add(-pts)
		waitTime = 0
	}

	return waitTime
}

func newTrackOutputSynchronizer(os *OutputSynchronizer) *TrackOutputSynchronizer {
	return &TrackOutputSynchronizer{
		os:     os,
		closed: core.NewFuse(),
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
