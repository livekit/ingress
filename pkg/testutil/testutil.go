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

// Package testutil holds helpers for tests only; it must not be imported
// from production code.
package testutil

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// ShortTempDir returns a directory under /tmp for tests that create unix
// sockets: t.TempDir() paths exceed the socket path limit (104 bytes on
// darwin).
func ShortTempDir(t *testing.T) string {
	tmpDir, err := os.MkdirTemp("/tmp", "ingress-test")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	return tmpDir
}
