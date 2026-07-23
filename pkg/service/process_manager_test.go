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

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/testutil"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
)

func newTestProcessManager(t *testing.T) (*ProcessManager, psrpc.MessageBus) {
	bus := psrpc.NewLocalMessageBus()
	sm := NewSessionManager(stats.NewMonitor(), nil)
	pm, err := NewProcessManager(sm, nil, bus, nil)
	require.NoError(t, err)
	return pm, bus
}

func newTestParams(tmpDir string) *params.Params {
	return &params.Params{
		IngressInfo: &livekit.IngressInfo{
			IngressId: "in_test",
			State:     &livekit.IngressState{ResourceId: "res_test"},
		},
		TmpDir: tmpDir,
	}
}

// requireNoResidue asserts the all-or-nothing property of startIngress: a
// failed start must leave no temp directory, no active handler, and no RPC
// topics on the bus.
func requireNoResidue(t *testing.T, pm *ProcessManager, bus psrpc.MessageBus, tmpDir string) {
	t.Helper()

	// not os.IsNotExist: stat fails with ENOTDIR rather than ENOENT when a
	// path component is not a directory
	_, err := os.Stat(tmpDir)
	require.Error(t, err, "temp directory should not exist")

	pm.mu.RLock()
	require.Empty(t, pm.activeHandlers)
	pm.mu.RUnlock()

	client, err := rpc.NewIngressHandlerClient(bus)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = client.DeleteIngress(ctx, "in_test", &livekit.DeleteIngressRequest{},
		psrpc.WithRequestTimeout(300*time.Millisecond))
	require.Error(t, err)
	require.True(t,
		errors.Is(err, psrpc.ErrRequestTimedOut) || errors.Is(err, psrpc.ErrNoResponse),
		"expected no server for the topic, got: %v", err)
}

func TestStartIngressRollsBackOnServiceSocketFailure(t *testing.T) {
	pm, bus := newTestProcessManager(t)
	tmpDir := filepath.Join(testutil.ShortTempDir(t), "session")

	// occupy the service socket path with a non-empty directory so the
	// pre-listen cleanup fails and the socket bind returns an error
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "service_ipc.sock", "x"), 0755))

	err := pm.startIngress(context.Background(), newTestParams(tmpDir), nil)
	require.Error(t, err)

	requireNoResidue(t, pm, bus, tmpDir)
}

func TestStartIngressFailsCleanlyWhenTmpDirCannotBeCreated(t *testing.T) {
	pm, bus := newTestProcessManager(t)

	// a regular file where the temp directory's parent should be makes
	// MkdirAll fail
	parent := filepath.Join(testutil.ShortTempDir(t), "file")
	require.NoError(t, os.WriteFile(parent, []byte("x"), 0o644))
	tmpDir := filepath.Join(parent, "session")

	err := pm.startIngress(context.Background(), newTestParams(tmpDir), nil)
	require.Error(t, err)

	requireNoResidue(t, pm, bus, tmpDir)
}
