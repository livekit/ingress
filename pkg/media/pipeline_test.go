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
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/ingress/pkg/utils"
	"github.com/livekit/protocol/livekit"
)

const testSystemMemoryCaps = "video/x-raw,format=NV12,width=1280,height=720,framerate=30/1"

// Caps carrying no resolution, which AddTrack reads before it touches the sink.
const testCapsWithoutResolution = "video/x-raw,format=NV12"

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

// Pipeline shutdown races its own startup: Handler.HandleIngress starts Run on
// a goroutine and calls SendEOS from its kill watcher, so a kill arriving during
// startup drives both against one Pipeline. The tests below cover a shutdown
// that reaches the pipeline before Run does. It must not find a nil loop, must
// stop Run before it starts anything, and its quit must survive until the loop
// runs.

const eosChildEnv = "INGRESS_EOS_RACE_CHILD"

// newTestPipeline builds the parts of a Pipeline these tests exercise, minus
// the ones that need a room connection.
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

// stubSource is the smallest Source that lets Run reach its main loop; the real
// ones all want a network peer. Start records that it ran and, when block is
// set, waits there, which is how a test holds Run inside startup.
type stubSource struct {
	started atomic.Bool
	entered chan struct{}
	block   chan struct{}
}

func (s *stubSource) GetSources() []*gst.Element   { return nil }
func (s *stubSource) ValidateCaps(*gst.Caps) error { return nil }
func (s *stubSource) Close() error                 { return nil }

func (s *stubSource) Start(context.Context, func()) error {
	s.started.Store(true)
	if s.entered != nil {
		close(s.entered)
	}
	if s.block != nil {
		<-s.block
	}

	return nil
}

// runnableTestPipeline adds the two collaborators Run walks through, stubbed to
// the minimum that lets it both reach and leave the loop.
func runnableTestPipeline(t *testing.T) (*Pipeline, *stubSource) {
	t.Helper()

	p := newTestPipeline(t)

	src := &stubSource{}
	p.input = &Input{source: src}

	sink := &WebRTCSink{}
	sink.sdkReady.Break() // Close blocks on this
	p.sink = sink

	return p, src
}

// A shutdown landing before Run must still leave the loop stoppable. Runs in a
// child process: SendEOS quits from a goroutine it spawns, and a panic there
// cannot be recovered by the test, so it has to be observed as a dead process
// rather than a failed assertion.
func TestSendEOSBeforeRunIsHonored(t *testing.T) {
	if os.Getenv(eosChildEnv) == "1" {
		p := newTestPipeline(t)

		// The race: EOS lands before Run has started the loop.
		p.SendEOS(context.Background())

		// SendEOS queues its quit from a goroutine, once the pipeline has gone
		// to NULL. Wait until the source is actually on the context, so this
		// exercises a quit that precedes Run rather than one that happens to
		// land while the loop is already running. A sleep would not tell the
		// two apart. The child runs this test alone, so nothing else has left
		// sources on the default context.
		require.Eventually(t, glib.MainContextDefault().Pending,
			5*time.Second, 5*time.Millisecond, "the quit was never queued")
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

// A shutdown that lands before Run must stop it before it starts anything.
// Starting the input would be unrecoverable: the real sources block on a
// network peer, and Run would sit there with the shutdown already spent.
func TestRunBailsWhenShutdownArrivedFirst(t *testing.T) {
	p, src := runnableTestPipeline(t)

	p.SendEOS(context.Background())

	done := make(chan error, 1)
	go func() { done <- p.Run(context.Background()) }()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return: it started work a spent shutdown cannot undo")
	}

	require.False(t, src.started.Load(), "the input must not be started")
	require.False(t, p.loop.IsRunning(), "the loop must not be started")
}

// The other window: the shutdown lands while Run is inside startup, past the
// bail and short of the loop. Run has to reach the loop and be stopped by the
// queued quit. Driving the real Run is what makes this a regression test for
// the loop moving back out of New.
func TestRunSurvivesTheStartupRace(t *testing.T) {
	p, src := runnableTestPipeline(t)
	src.entered = make(chan struct{})
	src.block = make(chan struct{})

	done := make(chan error, 1)
	go func() { done <- p.Run(context.Background()) }()

	// Hold Run inside input.Start, which is where the real sources block, and
	// shut down from there.
	<-src.entered
	p.SendEOS(context.Background())

	// SendEOS queues its quit from a goroutine. Wait until the source is on
	// the context before releasing Run, so the quit is reliably the one that
	// precedes the loop rather than one that lands while it is already
	// running -- the case that always worked.
	require.Eventually(t, glib.MainContextDefault().Pending,
		5*time.Second, 5*time.Millisecond, "the quit was never queued")
	require.False(t, p.loop.IsRunning(), "loop must not have started yet")

	close(src.block)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return: the queued quit never reached its loop")
	}
}

// A shutdown arriving once Run is already in the loop is the ordinary case and
// must still stop it.
func TestRunStopsWhenShutdownArrivesLater(t *testing.T) {
	p, _ := runnableTestPipeline(t)

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
		"a direct quit before Run is expected to be lost; if this passes, "+
			"quitLoop's idle source may be unnecessary")

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

func newTestParams(t *testing.T) *params.Params {
	t.Helper()

	info := &livekit.IngressInfo{
		IngressId:           "IN_test",
		Name:                "test",
		StreamKey:           "streamkey",
		InputType:           livekit.IngressInput_RTMP_INPUT,
		RoomName:            "room",
		ParticipantIdentity: "identity",
		ParticipantName:     "name",
		State:               &livekit.IngressState{ResourceId: "RS_test"},
	}

	conf := &config.Config{ServiceConfig: &config.ServiceConfig{}, InternalConfig: &config.InternalConfig{}}

	p, err := params.GetParams(context.Background(), utils.NewNoopStateNotifier(), conf, info,
		"ws://localhost:7880", "token", "project", "relay", nil, nil, nil)
	require.NoError(t, err)

	return p
}

// A session that cannot build one of its outputs reports a terminal status, and
// downstream reads that as the session having ended, so it has to stop rather
// than keep running unreported.
//
// The sink is nil: AddTrack reads the resolution before it touches the sink, so
// caps without one reach the real failure path. A reordering there panics
// instead of silently passing.
func TestTrackBuildFailureStopsTheSession(t *testing.T) {
	gst.Init(nil)

	p := &Pipeline{
		Params:      newTestParams(t),
		loop:        glib.NewMainLoop(glib.MainContextDefault(), false),
		pipelineErr: make(chan error, 1),
		established: make(map[types.StreamKind]string),
	}

	go p.loop.Run()
	require.Eventually(t, p.loop.IsRunning, 2*time.Second, 10*time.Millisecond, "loop did not start")

	p.onParamsReady(types.Video, newCapsHoldingGhostPad(t, testCapsWithoutResolution))

	require.Equal(t, livekit.IngressState_ENDPOINT_ERROR, p.State.Status)
	require.Empty(t, p.established, "a failed output must not be recorded as built")
	require.Eventually(t, func() bool { return !p.loop.IsRunning() }, 2*time.Second, 10*time.Millisecond,
		"the session kept running after a track failed to build")

	select {
	case err := <-p.pipelineErr:
		require.Error(t, err, "Run must end with the track failure as its cause")
	default:
		t.Fatal("no error was handed to Run, so the session would end as a clean shutdown")
	}
}
