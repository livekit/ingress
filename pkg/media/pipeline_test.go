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

package media

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/logger"
)

const testSystemMemoryCaps = "video/x-raw,format=NV12,width=1280,height=720,framerate=30/1"

// newCapsHoldingGhostPad returns a src ghost pad carrying capsStr, the shape
// Input surfaces. A pad's caps property is its sticky CAPS event, and an
// inactive pad is flushing, so the pad is activated before the event is stored.
func newCapsHoldingGhostPad(t *testing.T, capsStr string) *gst.GhostPad {
	t.Helper()

	ghost := gst.NewGhostPadNoTarget("video", gst.PadDirectionSource)
	require.True(t, ghost.SetActive(true))
	require.Equal(t, gst.FlowOK,
		ghost.StoreStickyEvent(gst.NewCapsEvent(gst.NewCapsFromString(capsStr))))
	require.NotNil(t, ghost.GetCurrentCaps())

	return ghost
}

// The renegotiation this fix exists for. On GStreamer 1.28 the video pad is
// advertised as memory:GLMemory and then renegotiated to system memory, so
// onParamsReady runs twice for one pad. The second run must not build another
// output: the bin name is hardcoded, so gst_bin_add would reject it.
//
// sink is deliberately left nil. Building an output dereferences it, so a
// regression here fails loudly instead of silently rebuilding.
func TestSecondCapsNotificationDoesNotRebuild(t *testing.T) {
	gst.Init(nil)

	const builtCaps = "video/x-raw(memory:GLMemory),format=NV12,width=1280,height=720,texture-target=rectangle"

	p := &Pipeline{
		established: map[types.StreamKind]string{types.Video: builtCaps},
	}

	p.onParamsReady(types.Video, newCapsHoldingGhostPad(t, testSystemMemoryCaps))

	require.Equal(t, builtCaps, p.established[types.Video],
		"the established caps must survive a renegotiation")
	require.Len(t, p.established, 1)
}

// A notification carrying no caps is not a renegotiation and must not record
// anything, or the real caps that follow would be treated as the second one.
func TestCapsNotificationWithoutCapsIsIgnored(t *testing.T) {
	gst.Init(nil)

	p := &Pipeline{
		established: make(map[types.StreamKind]string),
	}

	p.onParamsReady(types.Video, gst.NewGhostPadNoTarget("video", gst.PadDirectionSource))

	require.Empty(t, p.established)
}

// CS-1376. Handler.HandleIngress starts Run on a goroutine and calls SendEOS
// from its kill watcher, so a DeleteIngress arriving during startup runs both
// against the same Pipeline. Two failures came out of that:
//
//   - Run used to create p.loop, so SendEOS could dereference a nil loop and
//     take the handler process down with it.
//   - Even once the loop existed, a direct Quit issued before Run reached
//     loop.Run() was discarded, and the handler hung on a loop nobody would
//     stop again. That one is silent, which makes it the worse of the two.
//
// New now builds the loop, and quitLoop queues the quit as an idle source so it
// survives until the loop starts.

const eosChildEnv = "INGRESS_EOS_RACE_CHILD"

// The warning Run emits when the shutdown beat it to the loop.
const raceWarning = "shutdown requested before the main loop started"

// newTestPipeline is the state New leaves a Pipeline in, minus the parts that
// need a room connection. The loop matters here: it is what New now owns.
func newTestPipeline(t *testing.T) *Pipeline {
	t.Helper()

	gst.Init(nil)

	pipeline, err := gst.NewPipeline("pipeline")
	require.NoError(t, err)

	return &Pipeline{
		pipeline:    pipeline,
		loop:        glib.NewMainLoop(glib.MainContextDefault(), false),
		pipelineErr: make(chan error, 1),
		eos:         newEOSDispatcher(),
	}
}

// startLoop starts the loop and returns a channel closed once it stops. The
// channel is returned rather than waited on here so that a test can observe the
// same run twice: a loop that is already running must not be started again.
func startLoop(p *Pipeline) <-chan struct{} {
	returned := make(chan struct{})
	go func() {
		p.loop.Run()
		close(returned)
	}()

	return returned
}

func stoppedWithin(returned <-chan struct{}, timeout time.Duration) bool {
	select {
	case <-returned:
		return true
	case <-time.After(timeout):
		return false
	}
}

// runLoop starts the loop and reports whether it stopped within the timeout.
func runLoop(p *Pipeline, timeout time.Duration) bool {
	return stoppedWithin(startLoop(p), timeout)
}

// waitRunning blocks until the loop is running, and gives up rather than
// spinning forever so a pipeline that never starts one fails instead of hanging.
func waitRunning(t *testing.T, l *glib.MainLoop) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for !l.IsRunning() {
		if time.Now().After(deadline) {
			t.Fatal("loop never started")
		}
		time.Sleep(time.Millisecond)
	}
}

// stubSource is the smallest Source that lets Run reach its main loop. The real
// ones all want a network peer.
type stubSource struct{}

func (stubSource) GetSources() []*gst.Element          { return nil }
func (stubSource) ValidateCaps(*gst.Caps) error        { return nil }
func (stubSource) Start(context.Context, func()) error { return nil }
func (stubSource) Close() error                        { return nil }

// runnableTestPipeline adds the two collaborators Run walks through, stubbed to
// the minimum that lets it both reach and leave the loop.
func runnableTestPipeline(t *testing.T) *Pipeline {
	t.Helper()

	p := newTestPipeline(t)
	p.input = &Input{source: stubSource{}}

	sink := &WebRTCSink{}
	sink.sdkReady.Break() // Close blocks on this
	p.sink = sink

	return p
}

// The regression for both halves of the bug, on the real SendEOS path. Runs in
// a child process because the nil dereference happened on a goroutine SendEOS
// spawns, and no parent can recover a panic raised on another goroutine: before
// the fix the process died outright rather than failing an assertion.
func TestSendEOSBeforeRunIsHonored(t *testing.T) {
	if os.Getenv(eosChildEnv) == "1" {
		p := newTestPipeline(t)

		// The race: EOS lands before Run has started the loop.
		p.SendEOS(context.Background())

		// SendEOS issues its quit from a goroutine, once the pipeline has gone
		// to NULL. Wait for that to have happened before starting the loop, so
		// the quit is reliably the early one this test is about; without the
		// wait the loop is often already running by then and the race is not
		// exercised at all. Going to NULL on an empty pipeline takes
		// microseconds, so this is a wide margin, not a tuned one.
		time.Sleep(500 * time.Millisecond)
		require.False(t, p.loop.IsRunning(), "loop must not have started yet")

		require.True(t, runLoop(p, 10*time.Second),
			"loop.Run did not return: the queued EOS was lost")
		return
	}

	out, err := runChild(t, "TestSendEOSBeforeRunIsHonored")

	require.NoError(t, err, "child process failed:\n%s", out)
	require.NotContains(t, string(out), "panic:", "child panicked:\n%s", out)
}

// The queuing on its own, without SendEOS's timers deciding when the quit is
// issued. This pins the ordering the fix depends on: quit first, loop second.
func TestQuitLoopBeforeRunIsHonored(t *testing.T) {
	p := newTestPipeline(t)

	p.quitLoop()

	require.True(t, runLoop(p, 5*time.Second),
		"a quit queued before Run must still stop the loop")
}

// quitLoop is also reached from the sink's close callback and from SendEOS's
// timeout goroutine, so it has to tolerate being called more than once and from
// several goroutines. Meaningful under -race.
func TestQuitLoopIsSafeConcurrently(t *testing.T) {
	p := newTestPipeline(t)

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.quitLoop()
		}()
	}
	wg.Wait()

	require.True(t, runLoop(p, 5*time.Second), "loop must still stop")
}

// SendEOS is fused, but the fuse only stops the body running twice; it does not
// order SendEOS against Run. Guards that the fused path still stops the loop.
func TestSendEOSTwiceStillStopsTheLoop(t *testing.T) {
	p := newTestPipeline(t)

	p.SendEOS(context.Background())
	p.SendEOS(context.Background())

	require.True(t, runLoop(p, 10*time.Second), "loop must still stop")
}

// Run itself, on the racing path: the shutdown lands first, and Run has to
// reach the loop, be stopped by the queued quit, and report the race. Driving
// the real Run is what makes this a regression test for the loop moving back
// out of New, which the tests above cannot see. Child process, because the
// assertion is on log output and the default logger discards.
func TestRunReportsAndSurvivesTheStartupRace(t *testing.T) {
	if os.Getenv(eosChildEnv) == "1" {
		logger.InitFromConfig(&logger.Config{Level: "debug"}, "ingress")

		p := runnableTestPipeline(t)

		p.SendEOS(context.Background())
		time.Sleep(500 * time.Millisecond)
		require.False(t, p.loop.IsRunning(), "loop must not have started yet")

		done := make(chan error, 1)
		go func() { done <- p.Run(context.Background()) }()

		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return: the queued quit never reached its loop")
		}
		return
	}

	out, err := runChild(t, "TestRunReportsAndSurvivesTheStartupRace")

	require.NoError(t, err, "child process failed:\n%s", out)
	require.Contains(t, string(out), raceWarning,
		"the startup race should be reported:\n%s", out)
}

// The warning has to be specific to the race or it is noise: a shutdown that
// arrives once Run is already in the loop is ordinary, and must stay quiet.
func TestRunIsQuietWhenShutdownArrivesLater(t *testing.T) {
	if os.Getenv(eosChildEnv) == "1" {
		logger.InitFromConfig(&logger.Config{Level: "debug"}, "ingress")

		p := runnableTestPipeline(t)

		done := make(chan error, 1)
		go func() { done <- p.Run(context.Background()) }()

		waitRunning(t, p.loop)
		p.SendEOS(context.Background())

		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return")
		}
		return
	}

	out, err := runChild(t, "TestRunIsQuietWhenShutdownArrivesLater")

	require.NoError(t, err, "child process failed:\n%s", out)
	require.NotContains(t, string(out), raceWarning,
		"a shutdown after the loop started is not the race:\n%s", out)
}

// Why quitLoop queues rather than calling Quit directly. g_main_loop_run sets
// is_running itself, so a direct Quit arriving first is overwritten and lost:
// the flag records "not running", never "was asked to stop". This is GStreamer
// behavior rather than ours, so it is pinned here to justify the indirection
// and to catch it changing under us.
func TestDirectQuitBeforeRunIsLost(t *testing.T) {
	p := newTestPipeline(t)

	p.loop.Quit()

	returned := startLoop(p)

	require.False(t, stoppedWithin(returned, 2*time.Second),
		"a direct quit before Run is expected to be lost; if this now passes, "+
			"quitLoop's idle source may no longer be needed")

	// Confirms the loop really is running, rather than merely unscheduled.
	require.True(t, p.loop.IsRunning())

	// Stop the run started above; do not start a second one.
	p.loop.Quit()
	require.True(t, stoppedWithin(returned, 5*time.Second),
		"loop did not stop on the second quit")
}

func runChild(t *testing.T, testName string) ([]byte, error) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=^"+testName+"$", "-test.v")
	cmd.Env = append(os.Environ(), eosChildEnv+"=1")

	return cmd.CombinedOutput()
}
