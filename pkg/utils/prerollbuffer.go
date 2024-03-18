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
	"bytes"
	"io"
	"sync"

	"github.com/livekit/ingress/pkg/errors"
)

const (
	maxBufferSize = 10000000
)

type PrerollBuffer struct {
	lock   sync.Mutex
	buffer *bytes.Buffer
	w      io.WriteCloser

	onBufferReset func() error
}

func NewPrerollBuffer(onBufferReset func() error) *PrerollBuffer {
	return &PrerollBuffer{
		buffer:        &bytes.Buffer{},
		onBufferReset: onBufferReset,
	}
}

func (pb *PrerollBuffer) SetWriter(w io.WriteCloser) error {
	pb.lock.Lock()
	defer pb.lock.Unlock()

	pb.w = w
	if pb.w == nil {
		// Send preroll buffer reset event
		pb.buffer.Reset()
		if pb.onBufferReset != nil {
			if err := pb.onBufferReset(); err != nil {
				return err
			}
		}
	} else {
		_, err := io.Copy(pb.w, pb.buffer)
		if err != nil {
			return err
		}
	}

	return nil
}

func (pb *PrerollBuffer) Write(p []byte) (int, error) {
	pb.lock.Lock()
	defer pb.lock.Unlock()

	if pb.w == nil {
		if len(p)+pb.buffer.Len() > maxBufferSize {
			// We would overflow the max allowed buffer size. Reset th buffer state
			pb.buffer.Reset()
			if pb.onBufferReset != nil {
				if err := pb.onBufferReset(); err != nil {
					return 0, err
				}
			}
			return 0, errors.ErrPrerollBufferReset
		}
		return pb.buffer.Write(p)
	}

	n, err := pb.w.Write(p)
	if err == io.ErrClosedPipe {
		// Do not return errors caused by a consuming pipe getting closed
		err = nil
	}
	return n, err
}

func (pb *PrerollBuffer) Close() error {
	pb.lock.Lock()
	defer pb.lock.Unlock()

	if pb.w != nil {
		return pb.w.Close()
	}

	return nil
}
