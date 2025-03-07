// Copyright 2024 LiveKit, Inc.
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

package lksdk_output

import (
	"sync"
	"time"
)

type TrackWatchdog struct {
	deadline time.Duration
	onFire   func()

	trackLock          sync.Mutex
	expectedTrackCount int
	boundTrackCount    int
	timer              *time.Timer
	started            bool
}

func NewTrackWatchdog(onFire func(), deadline time.Duration) *TrackWatchdog {
	return &TrackWatchdog{
		onFire:   onFire,
		deadline: deadline,
		started:  true,
	}
}

func (w *TrackWatchdog) TrackAdded() {
	w.trackLock.Lock()
	defer w.trackLock.Unlock()

	w.expectedTrackCount++

	w.updateTimer()
}

func (w *TrackWatchdog) TrackBound() {
	w.trackLock.Lock()
	defer w.trackLock.Unlock()

	w.boundTrackCount++

	w.updateTimer()
}

func (w *TrackWatchdog) TrackUnbound() {
	w.trackLock.Lock()
	defer w.trackLock.Unlock()

	w.boundTrackCount--

	w.updateTimer()
}

func (w *TrackWatchdog) Stop() {
	w.trackLock.Lock()
	defer w.trackLock.Unlock()

	w.started = false

	w.updateTimer()
}

// Must be called locked
func (w *TrackWatchdog) updateTimer() {
	timerMustBeActive := w.started && w.boundTrackCount < w.expectedTrackCount

	if w.timer == nil && timerMustBeActive {
		w.timer = time.AfterFunc(w.deadline, w.onFire)
	}

	if w.timer != nil && !timerMustBeActive {
		w.timer.Stop()
		w.timer = nil
	}
}
