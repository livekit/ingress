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
	"fmt"
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
	maxPts   time.Duration
	closed   core.Fuse
}

func NewOutputSynchronizer() *OutputSynchronizer {
	return &OutputSynchronizer{
		closed: core.NewFuse(),
	}
}

func (os *OutputSynchronizer) Close() {
	os.closed.Break()
}

func (os *OutputSynchronizer) getWaitDuration(pts time.Duration) time.Duration {
	os.lock.Lock()
	defer os.lock.Unlock()

	mediaTime := os.zeroTime.Add(pts)
	now := time.Now()

	waitTime := mediaTime.Sub(now)

	if os.zeroTime.IsZero() || (-waitTime > leeway && pts >= os.maxPts) {
		// Reset zeroTime
		os.zeroTime = now.Add(-pts)
		waitTime = 0
	}

	if pts > os.maxPts {
		os.maxPts = pts
	}

	if -waitTime > 100*time.Millisecond {
		fmt.Println("WAITTIME", waitTime)
	}

	return waitTime
}

func (os *OutputSynchronizer) WaitForMediaTime(pts time.Duration) (bool, error) {
	waitTime := os.getWaitDuration(pts)

	if -waitTime > 100*time.Millisecond {
		return true, nil
	}

	select {
	case <-time.After(waitTime):
		return false, nil
	case <-os.closed.Watch():
		return false, errors.ErrIngressClosing
	}
}
