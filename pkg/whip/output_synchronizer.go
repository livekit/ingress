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

package whip

import (
	"context"
	"sync"
	"time"

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/types"
)

const (
	leeway = 100 * time.Millisecond
)

type OutputSynchronizer struct {
	lock sync.Mutex

	zeroTime time.Time
	tracks   map[types.StreamKind]*TrackOutputSynchronizer
}

type TrackOutputSynchronizer struct {
	outputSynchronizer *OutputSynchronizer
	closed             core.Fuse
}

func NewOutputSynchronizer() *OutputSynchronizer {
	return &OutputSynchronizer{
		tracks: make(map[types.StreamKind]*TrackOutputSynchronizer),
	}
}

func (os *OutputSynchronizer) AddTrack(trackType types.StreamKind) *TrackOutputSynchronizer {
	ts := newTrackOutputSynchronizer(os)

	os.lock.Lock()
	defer os.lock.Unlock()

	os.tracks[trackType] = ts

	return ts
}

func (os *OutputSynchronizer) Close() {
	os.lock.Lock()
	defer os.lock.Unlock()

	for _, t := range os.tracks {
		t.Close()
	}
}

func (os *OutputSynchronizer) getWaitDuration(pts time.Duration) time.Duration {
	os.lock.Lock()
	defer os.lock.Unlock()

	mediaTime := os.zeroTime.Add(pts)
	now := time.Now()

	waitTime := mediaTime.Sub(now)

	if -waitTime > leeway {
		// Reset zeroTime
		os.zeroTime = now.Add(-pts)
		waitTime = 0
	}

	return waitTime
}

func newTrackOutputSynchronizer(os *OutputSynchronizer) *TrackOutputSynchronizer {
	return &TrackOutputSynchronizer{
		outputSynchronizer: os,
		closed:             core.NewFuse(),
	}
}

func (ts *TrackOutputSynchronizer) WaitForMediaTime(ctx context.Context, pts time.Duration) error {
	waitTime := ts.outputSynchronizer.getWaitDuration(pts)

	select {
	case <-time.After(waitTime):
		return nil
	case <-ctx.Done():
		return errors.ErrIngressClosing
	case <-ts.closed.Watch():
		return errors.ErrIngressClosing
	}
}

func (ts *TrackOutputSynchronizer) Close() {
	ts.closed.Break()
}
