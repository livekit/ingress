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

package lksdk_output

import (
	"sync/atomic"
	"time"

	"github.com/frostbyte73/core"
)

type MediaWatchdog struct {
	bytesSinceLastCheck atomic.Int64

	done core.Fuse
}

func NewMediaWatchdog(onFire func(), deadline time.Duration) *MediaWatchdog {
	w := &MediaWatchdog{}

	ticker := time.NewTicker(deadline)

	go func() {
		for {
			select {
			case <-w.done.Watch():
				ticker.Stop()
				return
			case <-ticker.C:
				b := w.bytesSinceLastCheck.Swap(0)
				if b == 0 {
					// Do not fire the watchdog again
					ticker.Stop()
					onFire()
				}
			}
		}
	}()

	return w
}

func (w *MediaWatchdog) Stop() {
	w.done.Break()
}

func (w *MediaWatchdog) MediaReceived(b int64) {
	w.bytesSinceLastCheck.Add(b)
}
