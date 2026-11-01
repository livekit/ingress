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
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
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

const (
	burnCPUEnv = "INGRESS_TEST_BURN_CPU"
	// burnCPU spans at least one tick of the 1s per-process sampler
	burnCPU = 2 * time.Second
)

// TestHelperBurnCPU is the handler stand-in run by TestSessionUsage: it uses burnCPU of CPU, then exits.
func TestHelperBurnCPU(t *testing.T) {
	if os.Getenv(burnCPUEnv) == "" {
		t.Skip("runs only as the child of TestSessionUsage")
	}
	var ru syscall.Rusage
	for time.Duration(ru.Utime.Nano()+ru.Stime.Nano()) < burnCPU {
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	}
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
	require.GreaterOrEqual(t, u.CPUTime, burnCPU, "the handler used burnCPU of CPU")
	require.Positive(t, u.Duration)
	require.Positive(t, u.AvgCPU())
	if runtime.GOOS == "linux" {
		// per-process sampling reads procfs
		require.Positive(t, u.MaxCPU)
		require.NotZero(t, u.MaxMemory)
	}

	require.Nil(t, m.SessionEnded("res_test"), "usage is reported once")
}

func TestSessionUsageTracks(t *testing.T) {
	m := NewMonitor()

	info := &livekit.IngressInfo{
		IngressId:         "IN_test",
		InputType:         livekit.IngressInput_URL_INPUT,
		EnableTranscoding: proto.Bool(true),
		State:             &livekit.IngressState{ResourceId: "res_test"},
	}

	// an update for a session with no usage record does not create one
	update := proto.Clone(info).(*livekit.IngressInfo)
	update.State.Audio = &livekit.InputAudioState{MimeType: "audio/aac"}
	m.HandlerStateUpdated(update)
	require.Nil(t, m.SessionEnded("res_test"))

	m.HandlerStarted(info, 1)
	m.HandlerStateUpdated(update)
	m.HandlerExited(1, nil)

	u := m.SessionEnded("res_test")
	require.NotNil(t, u)
	require.Equal(t, "IN_test", u.IngressID)
	require.Equal(t, livekit.IngressInput_URL_INPUT, u.InputType)
	require.True(t, u.Transcoding)
	require.Equal(t, "audio/aac", u.AudioMimeType)
	require.Empty(t, u.VideoMimeType, "no video track was reported")

	// a later update adds video and keeps the audio type
	m.HandlerStarted(info, 2)
	update.State.Video = &livekit.InputVideoState{MimeType: "video/H264"}
	m.HandlerStateUpdated(update)
	update.State.Video = nil
	m.HandlerStateUpdated(update)
	m.HandlerExited(2, nil)

	u = m.SessionEnded("res_test")
	require.NotNil(t, u)
	require.Equal(t, "audio/aac", u.AudioMimeType)
	require.Equal(t, "video/H264", u.VideoMimeType, "a reported track stays recorded")

	// a session that ends before the source is parsed reports no tracks
	m.HandlerStarted(info, 3)
	m.HandlerExited(3, nil)
	u = m.SessionEnded("res_test")
	require.NotNil(t, u)
	require.Empty(t, u.AudioMimeType)
	require.Empty(t, u.VideoMimeType)
}

func TestSessionUsageLabels(t *testing.T) {
	cases := []struct {
		name  string
		usage SessionUsage
		kind  string
		media string
	}{
		{"rtmp with both tracks", SessionUsage{InputType: livekit.IngressInput_RTMP_INPUT, Transcoding: true, AudioMimeType: "audio/aac", VideoMimeType: "video/H264"}, "rtmp", "av"},
		{"url audio only", SessionUsage{InputType: livekit.IngressInput_URL_INPUT, Transcoding: true, AudioMimeType: "audio/mpeg"}, "url", "audio"},
		{"rtmp video only", SessionUsage{InputType: livekit.IngressInput_RTMP_INPUT, Transcoding: true, VideoMimeType: "video/H264"}, "rtmp", "video"},
		{"whip transcoding, no tracks yet", SessionUsage{InputType: livekit.IngressInput_WHIP_INPUT, Transcoding: true}, "whip", "unknown"},
		{"whip bypass", SessionUsage{InputType: livekit.IngressInput_WHIP_INPUT, Transcoding: false, AudioMimeType: "audio/opus", VideoMimeType: "video/VP8"}, "whip_bypass", "av"},
		{"unset input type", SessionUsage{}, "unknown", "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.kind, c.usage.TypeLabel())
			require.Equal(t, c.media, c.usage.MediaLabel())
		})
	}
}

// endSession runs a session through the monitor, overrides its measured usage
// with the given values, and returns what SessionEnded reports for it.
func endSession(t *testing.T, m *Monitor, info *livekit.IngressInfo, pid int, maxCPU float64, cpu, dur time.Duration) *SessionUsage {
	t.Helper()

	m.HandlerStarted(info, pid)
	m.HandlerStateUpdated(info)
	m.HandlerExited(pid, nil)
	m.usageLock.Lock()
	u := m.usage[info.State.ResourceId]
	u.MaxCPU = maxCPU
	u.CPUTime = cpu
	u.Duration = dur
	m.usageLock.Unlock()

	ended := m.SessionEnded(info.State.ResourceId)
	require.NotNil(t, ended)
	return ended
}

// sessionMetric returns the sample of family with the given type and media
// labels from the default registry, or nil if it has none.
func sessionMetric(t *testing.T, family, kind, media string) *dto.Metric {
	t.Helper()

	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != family {
			continue
		}
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels[promLabelType] == kind && labels[promLabelMedia] == media {
				return m
			}
		}
	}
	return nil
}

func TestSessionMetrics(t *testing.T) {
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

	url := &livekit.IngressInfo{
		IngressId:         "IN_url",
		InputType:         livekit.IngressInput_URL_INPUT,
		EnableTranscoding: proto.Bool(true),
		State:             &livekit.IngressState{ResourceId: "res_url", Audio: &livekit.InputAudioState{MimeType: "audio/mpeg"}},
	}
	m.HandlerStarted(url, 1)
	m.HandlerStateUpdated(url)
	m.HandlerExited(1, nil)
	require.NotNil(t, m.SessionEnded("res_url"))

	// the media label comes from the tracks reported during the session
	require.NotNil(t, sessionMetric(t, "livekit_ingress_session_max_cpu_cores", "url", "audio"))
	require.Nil(t, sessionMetric(t, "livekit_ingress_session_max_cpu_cores", "url", "unknown"))

	// each ended session is one observation; the peak follows the highest
	// MaxCPU and never drops
	endSession(t, m, url, 2, 1.5, 2*time.Second, 4*time.Second)
	endSession(t, m, url, 3, 0.5, 1*time.Second, 4*time.Second)
	endSession(t, m, url, 4, 2.5, 3*time.Second, 4*time.Second)

	hist := sessionMetric(t, "livekit_ingress_session_max_cpu_cores", "url", "audio")
	require.NotNil(t, hist)
	require.EqualValues(t, 4, hist.GetHistogram().GetSampleCount())
	require.InDelta(t, 4.5, hist.GetHistogram().GetSampleSum(), 1e-9)
	require.NotEmpty(t, hist.GetHistogram().GetBucket(), "classic buckets are exported alongside the native schema")

	peak := sessionMetric(t, "livekit_ingress_session_max_cpu_cores_peak", "url", "audio")
	require.NotNil(t, peak)
	require.Equal(t, 2.5, peak.GetGauge().GetValue())

	// avg is CPU time over wall time: 0.5 + 0.25 + 0.75, plus the first
	// session's 0
	avg := sessionMetric(t, "livekit_ingress_session_avg_cpu_cores", "url", "audio")
	require.NotNil(t, avg)
	require.EqualValues(t, 4, avg.GetHistogram().GetSampleCount())
	require.InDelta(t, 1.5, avg.GetHistogram().GetSampleSum(), 1e-9)

	// a session that ends before any track is reported lands in unknown
	bypass := &livekit.IngressInfo{
		IngressId:         "IN_whip",
		InputType:         livekit.IngressInput_WHIP_INPUT,
		EnableTranscoding: proto.Bool(false),
		State:             &livekit.IngressState{ResourceId: "res_whip"},
	}
	endSession(t, m, bypass, 5, 0.05, 10*time.Millisecond, 200*time.Millisecond)
	require.NotNil(t, sessionMetric(t, "livekit_ingress_session_max_cpu_cores", "whip_bypass", "unknown"))

	// nothing is observed on a monitor that has not started
	fresh := NewMonitor()
	endSession(t, fresh, url, 6, 9, time.Second, time.Second)
	require.Equal(t, 2.5, sessionMetric(t, "livekit_ingress_session_max_cpu_cores_peak", "url", "audio").GetGauge().GetValue())
}
