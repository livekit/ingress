// Copyright 2026 LiveKit, Inc.
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

package gstutil_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/gstutil"
)

func TestSetProperty(t *testing.T) {
	queue, err := gstutil.NewElement("queue")
	require.NoError(t, err)
	fakesrc, err := gstutil.NewElement("fakesrc")
	require.NoError(t, err)

	require.NoError(t, gstutil.SetProperty(queue, "max-size-buffers", uint32(3)))
	require.Equal(t, uint32(3), queue.ObjectProperty("max-size-buffers"))

	// GLib converts between compatible types, so an int32 sets a uint property.
	require.NoError(t, gstutil.SetProperty(queue, "max-size-buffers", int32(4)))
	require.Equal(t, uint32(4), queue.ObjectProperty("max-size-buffers"))

	for name, set := range map[string]func() error{
		"missing":      func() error { return gstutil.SetProperty(queue, "no-such-property", uint32(1)) },
		"not writable": func() error { return gstutil.SetProperty(queue, "current-level-buffers", uint32(1)) },
		"wrong type":   func() error { return gstutil.SetProperty(queue, "max-size-buffers", "five") },
		"out of range": func() error { return gstutil.SetProperty(fakesrc, "num-buffers", int32(-5)) },
		"go int":       func() error { return gstutil.SetProperty(queue, "max-size-buffers", 5) },
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, set())
		})
	}
	require.Equal(t, uint32(4), queue.ObjectProperty("max-size-buffers"), "a rejected value was set")
	require.Equal(t, int32(-1), fakesrc.ObjectProperty("num-buffers"), "a rejected value was set")
}
