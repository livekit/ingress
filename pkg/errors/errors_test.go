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

package errors

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestHTTPError(t *testing.T) {
	inner := New("not found")
	err := NewHTTPError(http.StatusNotFound, inner)

	require.Equal(t, http.StatusNotFound, err.StatusCode)

	// Error() includes the status code.
	require.Equal(t, "HTTP error 404: not found", err.Error())

	// Unwrap() exposes the wrapped error for errors.Is/As.
	require.ErrorIs(t, err, inner)

	var httpErr *HTTPError
	require.True(t, As(err, &httpErr))
	require.Equal(t, http.StatusNotFound, httpErr.StatusCode)
}

func newResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestNewHTTPErrorFromResponse(t *testing.T) {
	t.Run("uses body as message", func(t *testing.T) {
		err := NewHTTPErrorFromResponse(newResponse(http.StatusBadRequest, "bad request payload"))
		require.Equal(t, http.StatusBadRequest, err.StatusCode)
		require.Equal(t, "HTTP error 400: bad request payload", err.Error())
	})

	t.Run("trims surrounding whitespace", func(t *testing.T) {
		err := NewHTTPErrorFromResponse(newResponse(http.StatusBadRequest, "  oops\n"))
		require.Equal(t, "HTTP error 400: oops", err.Error())
	})

	t.Run("empty body falls back to generic message", func(t *testing.T) {
		err := NewHTTPErrorFromResponse(newResponse(http.StatusInternalServerError, ""))
		require.Equal(t, "HTTP error 500: server returned an error", err.Error())
	})

	t.Run("whitespace-only body falls back to generic message", func(t *testing.T) {
		err := NewHTTPErrorFromResponse(newResponse(http.StatusInternalServerError, "   \n\t"))
		require.Equal(t, "HTTP error 500: server returned an error", err.Error())
	})

	t.Run("long body is truncated", func(t *testing.T) {
		body := strings.Repeat("a", maxErrorBodyLen+50)
		err := NewHTTPErrorFromResponse(newResponse(http.StatusBadGateway, body))
		expected := strings.Repeat("a", maxErrorBodyLen) + "... (truncated)"
		require.Equal(t, "HTTP error 502: "+expected, err.Error())
	})

	t.Run("body exactly at limit is not truncated", func(t *testing.T) {
		body := strings.Repeat("a", maxErrorBodyLen)
		err := NewHTTPErrorFromResponse(newResponse(http.StatusBadGateway, body))
		require.Equal(t, "HTTP error 502: "+body, err.Error())
	})

	t.Run("multibyte body truncates on rune boundary", func(t *testing.T) {
		// Each "é" is 2 bytes; ensure we count characters, not bytes, and never
		// split a rune.
		body := strings.Repeat("é", maxErrorBodyLen+50)
		err := NewHTTPErrorFromResponse(newResponse(http.StatusBadGateway, body))
		expected := strings.Repeat("é", maxErrorBodyLen) + "... (truncated)"
		require.Equal(t, "HTTP error 502: "+expected, err.Error())
		require.True(t, utf8.ValidString(err.Error()))
	})
}
