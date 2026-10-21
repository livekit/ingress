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

package stats

import (
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/psrpc"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
)

func TestPublicationStatus(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "nil error is success",
			err:      nil,
			expected: publicationStatusSuccess,
		},
		{
			name:     "unstructured error is internal",
			err:      errors.New("boom"),
			expected: publicationStatusInternal,
		},
		{
			name:     "HTTPError 4xx is 4xx",
			err:      errors.NewHTTPError(http.StatusBadRequest, errors.New("bad request")),
			expected: publicationStatus4xx,
		},
		{
			name:     "HTTPError 5xx is 5xx",
			err:      errors.NewHTTPError(http.StatusBadGateway, errors.New("upstream down")),
			expected: publicationStatus5xx,
		},
		{
			name:     "HTTPError non-error status is internal",
			err:      errors.NewHTTPError(http.StatusOK, errors.New("weird")),
			expected: publicationStatusInternal,
		},
		{
			name:     "HTTPError 5xx wrapped in psrpc is still 5xx",
			err:      psrpc.NewError(psrpc.Internal, errors.NewHTTPError(http.StatusServiceUnavailable, errors.New("unavailable"))),
			expected: publicationStatus5xx,
		},
		{
			name:     "psrpc 4xx code is 4xx",
			err:      psrpc.NewErrorf(psrpc.InvalidArgument, "bad input"),
			expected: publicationStatus4xx,
		},
		{
			name:     "psrpc internal code is internal",
			err:      psrpc.NewErrorf(psrpc.Internal, "boom"),
			expected: publicationStatusInternal,
		},
		{
			name:     "psrpc 5xx code is internal, not 5xx",
			err:      psrpc.NewErrorf(psrpc.Unavailable, "unavailable"),
			expected: publicationStatusInternal,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.expected, publicationStatus(c.err))
		})
	}
}

const burnCPUEnv = "INGRESS_TEST_BURN_CPU"

// TestHelperBurnCPU is the handler stand-in run by TestSessionUsage: it keeps one core busy for 1.5s.
func TestHelperBurnCPU(t *testing.T) {
	if os.Getenv(burnCPUEnv) == "" {
		return
	}
	for end := time.Now().Add(1500 * time.Millisecond); time.Now().Before(end); {
	}
	os.Exit(0)
}

func TestSessionUsage(t *testing.T) {
	conf, err := config.NewConfig(`
redis:
  address: localhost:6379
ws_url: ws://localhost:7880
api_key: key
api_secret: secret
cpu_cost:
  url_cpu_cost: 0.0001
`)
	require.NoError(t, err)

	m := NewMonitor()
	require.NoError(t, m.Start(conf))
	t.Cleanup(m.Stop)

	info := &livekit.IngressInfo{
		IngressId:         "IN_test",
		InputType:         livekit.IngressInput_URL_INPUT,
		EnableTranscoding: proto.Bool(true),
		State:             &livekit.IngressState{ResourceId: "res_test"},
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperBurnCPU$")
	cmd.Env = append(os.Environ(), burnCPUEnv+"=1")
	require.NoError(t, cmd.Start())
	m.HandlerStarted(info, cmd.Process.Pid)
	require.NoError(t, cmd.Wait())
	m.HandlerExited(cmd.Process.Pid, cmd.ProcessState)

	u := m.SessionEnded("res_test")
	require.NotNil(t, u)
	require.Equal(t, "IN_test", u.IngressID)
	require.Equal(t, livekit.IngressInput_URL_INPUT, u.InputType)
	require.True(t, u.Transcoding)
	require.GreaterOrEqual(t, u.Duration, 1500*time.Millisecond)
	require.Greater(t, u.AvgCPU(), 0.5, "the handler kept one core busy")
	if runtime.GOOS == "linux" {
		// per-process sampling reads procfs
		require.Greater(t, u.MaxCPU, 0.5)
		require.NotZero(t, u.MaxMemory)
	}

	require.Nil(t, m.SessionEnded("res_test"), "usage is reported once")
}
