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
	"sync/atomic"

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/errors"
)

type BlockingQueue[T any] struct {
	queue  chan T
	length atomic.Int32

	closed core.Fuse
}

func NewBlockingQueue[T any](capacity int) *BlockingQueue[T] {
	return &BlockingQueue[T]{
		queue: make(chan T, capacity),
	}
}

func (b *BlockingQueue[T]) PushBack(item T) {
	select {
	case b.queue <- item:
		// Small race here
		b.length.Add(1)
	case <-b.closed.Watch():
	}
}

func (b *BlockingQueue[T]) PopFront() (T, error) {
	select {
	case item := <-b.queue:
		// Small race here
		b.length.Add(-1)
		return item, nil
	case <-b.closed.Watch():
		return *new(T), errors.ErrIngressClosing
	}
}

func (b *BlockingQueue[T]) QueueLength() int {
	return int(b.length.Load())
}

func (b *BlockingQueue[T]) Close() {
	b.closed.Break()
}
