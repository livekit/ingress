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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Only the handler's own structured lines go to the sink.
func TestHandlerLoggerSink(t *testing.T) {
	var got []string
	l := NewHandlerLoggerWithSink("RES_abc", "IN_123", func(line []byte) {
		got = append(got, string(line))
	})

	_, err := l.Write([]byte(
		`{"level":"info","msg":"started"}` + "\n" +
			"0:00:00.1 gstreamer noise\n" +
			"ice ERROR: something\n" +
			`{"level":"error","msg":"failed"}` + "\n",
	))
	require.NoError(t, err)

	require.Equal(t, []string{
		`{"level":"info","msg":"started"}`,
		`{"level":"error","msg":"failed"}`,
	}, got)
}

func TestHandlerLoggerLineSplitAcrossWrites(t *testing.T) {
	var got []string
	l := NewHandlerLoggerWithSink("RES_abc", "IN_123", func(line []byte) {
		got = append(got, string(line))
	})

	for _, chunk := range []string{
		`{"level":"info","msg":"first"}` + "\n" + `{"level":"debug","ts":`,
		`1.5,"msg":"second"}` + "\n",
	} {
		_, err := l.Write([]byte(chunk))
		require.NoError(t, err)
	}

	require.Equal(t, []string{
		`{"level":"info","msg":"first"}`,
		`{"level":"debug","ts":1.5,"msg":"second"}`,
	}, got)
}

func TestHandlerLoggerCloseFlushesTrailingLine(t *testing.T) {
	var got []string
	l := NewHandlerLoggerWithSink("RES_abc", "IN_123", func(line []byte) {
		got = append(got, string(line))
	})

	_, err := l.Write([]byte(`{"level":"info","msg":"last"}`))
	require.NoError(t, err)
	require.Empty(t, got)

	require.NoError(t, l.Close())
	require.Equal(t, []string{`{"level":"info","msg":"last"}`}, got)
}

func TestHandlerLoggerCapsHeldLine(t *testing.T) {
	var got []string
	l := NewHandlerLoggerWithSink("RES_abc", "IN_123", func(line []byte) {
		got = append(got, string(line))
	})

	long := strings.Repeat("a", maxLineLength-1) + "}"
	_, err := l.Write([]byte(long))
	require.NoError(t, err)
	require.Equal(t, []string{long}, got)
}

func TestHandlerLoggerSinkAppendKeepsNextLine(t *testing.T) {
	var got []string
	l := NewHandlerLoggerWithSink("RES_abc", "IN_123", func(line []byte) {
		got = append(got, string(append(line, 'X', 'Y')))
	})

	_, err := l.Write([]byte("{\"a\":1}\n{\"b\":2}\n"))
	require.NoError(t, err)
	require.Equal(t, []string{`{"a":1}XY`, `{"b":2}XY`}, got)
}
