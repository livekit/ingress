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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/types"
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

// A pull that stops short of the duration its source advertises is how an
// upstream failure reaches us: adaptivedemux2 turns repeated segment fetch
// failures into an EOS no different from the one a finished source raises.
// These drive the check over a real pipeline, because what it reads is
// GStreamer's answer to a duration and a position query rather than anything
// computed here.

const testSourceLength = 20 * time.Second

// newSeekablePipeline writes a wav of the given length and returns a Pipeline
// reading it, prerolled so both queries answer. PAUSED is enough: a position
// query is served from the sink's preroll, so nothing plays out in real time.
func newSeekablePipeline(t *testing.T, length time.Duration) *Pipeline {
	t.Helper()

	gst.Init(nil)

	path := filepath.Join(t.TempDir(), "tone.wav")

	// One buffer per second, so num-buffers is the length in seconds.
	enc, err := gst.NewPipelineFromString(fmt.Sprintf(
		"audiotestsrc num-buffers=%d samplesperbuffer=44100 ! audioconvert ! wavenc ! filesink location=%s",
		int(length.Seconds()), path))
	require.NoError(t, err)
	require.NoError(t, enc.BlockSetState(gst.StatePlaying))

	msg := enc.GetPipelineBus().TimedPopFiltered(
		gst.ClockTime(30*time.Second), gst.MessageEOS|gst.MessageError)
	require.NotNil(t, msg, "writing the fixture timed out")
	require.Equal(t, gst.MessageEOS, msg.Type(), msg.String())
	require.NoError(t, enc.BlockSetState(gst.StateNull))

	read, err := gst.NewPipelineFromString(fmt.Sprintf(
		"filesrc location=%s ! wavparse ! fakesink sync=false", path))
	require.NoError(t, err)
	require.NoError(t, read.BlockSetState(gst.StatePaused))
	t.Cleanup(func() { _ = read.BlockSetState(gst.StateNull) })

	return &Pipeline{pipeline: read}
}

// stopAt leaves the pipeline reporting pos as its position. A flushing seek
// re-prerolls, so the new position is waited for rather than assumed.
func stopAt(t *testing.T, p *Pipeline, pos time.Duration) {
	t.Helper()

	require.True(t, p.pipeline.SeekTime(pos, gst.SeekFlagFlush|gst.SeekFlagAccurate),
		"seek to %s failed", pos)
	p.pipeline.GetState(gst.StatePaused, gst.ClockTime(10*time.Second))

	ok, got := p.pipeline.QueryPosition(gst.FormatTime)
	require.True(t, ok, "the pipeline reported no position after seeking")
	require.InDelta(t, pos.Seconds(), time.Duration(got).Seconds(), 0.5,
		"seek did not land where the test needs it")
}

// The reported case: the source stopped far short of its duration.
func TestSourceStoppingShortOfItsDurationIsAnError(t *testing.T) {
	p := newSeekablePipeline(t, testSourceLength)

	stopAt(t, p, 2*time.Second)

	err := p.checkSourceComplete()

	require.Error(t, err, "stopping 2s into %s must not be reported complete", testSourceLength)
	require.Contains(t, err.Error(), "source ended after")
}

// Playing out the whole source is the ordinary end and must stay complete.
func TestSourcePlayedToItsDurationIsComplete(t *testing.T) {
	p := newSeekablePipeline(t, testSourceLength)

	stopAt(t, p, testSourceLength)

	require.NoError(t, p.checkSourceComplete())
}

// Missing a slice smaller than the tolerance is the ordinary end of a source
// whose final segment ran shorter than the manifest declared.
func TestStoppingWithinTheToleranceIsComplete(t *testing.T) {
	const length = 60 * time.Second

	p := newSeekablePipeline(t, length)

	// 2s of 60s is under the tolerance.
	stopAt(t, p, length-2*time.Second)

	require.NoError(t, p.checkSourceComplete())
}

// The tolerance is a share of the source, not a fixed span, so the same
// shortfall means different things on different lengths. A fixed tolerance
// wide enough for the long source would call the short one complete too.
func TestToleranceIsProportionalToTheSource(t *testing.T) {
	const shortfall = 2 * time.Second

	short := newSeekablePipeline(t, testSourceLength)
	stopAt(t, short, testSourceLength-shortfall)
	require.Error(t, short.checkSourceComplete(),
		"%s of %s is past the tolerance", shortfall, testSourceLength)

	long := newSeekablePipeline(t, 60*time.Second)
	stopAt(t, long, 60*time.Second-shortfall)
	require.NoError(t, long.checkSourceComplete(),
		"%s of 1m0s is within the tolerance", shortfall)
}

// Live HLS along with the RTMP and WHIP inputs have no end to fall short of,
// so the check has to leave them alone.
//
// The two query assertions carry this test. GStreamer answers a duration query
// for such a source rather than declining it, and reports the unknown duration
// as a negative value, which is why the guard reads the value and not the
// boolean. Were that to change, the whole path would shift.
func TestSourceWithoutDurationIsComplete(t *testing.T) {
	gst.Init(nil)

	live, err := gst.NewPipelineFromString("audiotestsrc is-live=true ! fakesink sync=false")
	require.NoError(t, err)
	require.NoError(t, live.BlockSetState(gst.StatePlaying))
	t.Cleanup(func() { _ = live.BlockSetState(gst.StateNull) })

	p := &Pipeline{pipeline: live}

	ok, d := p.pipeline.QueryDuration(gst.FormatTime)
	require.True(t, ok, "the duration query is answered even with no duration to give")
	require.Negative(t, d, "an unknown duration is reported as a negative value")

	require.NoError(t, p.checkSourceComplete())
}

// The check hangs off the EOS branch of messageWatch, and two things there
// carry it: it has to run while the pipeline can still answer a position
// query, and it has to stay out of the way of a stop we asked for.

// newTruncatedPipeline returns a Pipeline sitting well short of its duration,
// wired with the parts messageWatch touches.
func newTruncatedPipeline(t *testing.T) *Pipeline {
	t.Helper()

	p := newSeekablePipeline(t, testSourceLength)
	p.loop = glib.NewMainLoop(glib.MainContextDefault(), false)
	p.pipelineErr = make(chan error, 1)
	stopAt(t, p, 2*time.Second)

	return p
}

// Driving the real branch also pins the ordering: the check has to precede the
// state change, since a NULL pipeline reports no position.
func TestEOSFromTheSourceQueuesTheTruncation(t *testing.T) {
	p := newTruncatedPipeline(t)

	p.messageWatch(gst.NewEOSMessage(p.pipeline))

	select {
	case err := <-p.pipelineErr:
		require.ErrorContains(t, err, "source ended after")
	default:
		t.Fatal("nothing queued: the check either did not run, " +
			"or ran once the pipeline could no longer report a position")
	}
}

// A stop we asked for ends playback early too. Reporting that as a truncated
// source would fail every ingress a caller deletes part way through.
func TestEOSAfterARequestedStopQueuesNothing(t *testing.T) {
	p := newTruncatedPipeline(t)

	p.closed.Break()

	p.messageWatch(gst.NewEOSMessage(p.pipeline))

	select {
	case err := <-p.pipelineErr:
		t.Fatalf("a stop we asked for was reported as a truncated source: %v", err)
	default:
	}
}

// The tests above read a wav through filesrc and wavparse, which is not the
// topology this check exists for: what a duration and a position query answer
// depends on which element answers them. These serve a real HLS fixture over
// HTTP through the same souphttpsrc, decodebin3 and hlsdemux2 chain a URL pull
// builds, so the manifest duration and the position of the last fragment that
// arrived are the real ones.

const (
	hlsFixtureSegments = 10
	hlsGoodSegments    = 3
)

// newHLSFixture writes a VOD playlist and its segments, and returns the
// directory along with the duration the playlist advertises.
func newHLSFixture(t *testing.T, segments int) (string, time.Duration) {
	t.Helper()

	gst.Init(nil)

	dir := t.TempDir()

	// One buffer per second at the default rate, so num-buffers is roughly the
	// segment count. max-files=0 keeps every segment; the default prunes them.
	enc, err := gst.NewPipelineFromString(fmt.Sprintf(
		"audiotestsrc num-buffers=%d samplesperbuffer=44100 ! audioconvert ! avenc_aac ! aacparse "+
			"! hlssink2 location=%s/seg%%05d.ts playlist-location=%s/playlist.m3u8 "+
			"target-duration=1 playlist-length=0 max-files=0",
		segments, dir, dir))
	require.NoError(t, err)
	require.NoError(t, enc.BlockSetState(gst.StatePlaying))

	msg := enc.GetPipelineBus().TimedPopFiltered(
		gst.ClockTime(60*time.Second), gst.MessageEOS|gst.MessageError)
	require.NotNil(t, msg, "writing the fixture timed out")
	require.Equal(t, gst.MessageEOS, msg.Type(), msg.String())
	require.NoError(t, enc.BlockSetState(gst.StateNull))

	playlist, err := os.ReadFile(filepath.Join(dir, "playlist.m3u8"))
	require.NoError(t, err)
	require.Contains(t, string(playlist), "#EXT-X-ENDLIST", "the fixture must be a VOD playlist")

	var advertised float64
	for _, line := range strings.Split(string(playlist), "\n") {
		v, ok := strings.CutPrefix(line, "#EXTINF:")
		if !ok {
			continue
		}
		d, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), ","), 64)
		require.NoError(t, err)
		advertised += d
	}
	require.NotZero(t, advertised, "the playlist declared no segments")

	return dir, time.Duration(advertised * float64(time.Second))
}

// serveHLS serves the fixture and answers segment requests with 500 once
// goodSegments have been handed out. The playlist keeps returning 200, so the
// only failure the demuxer sees is on a fragment, which is what turns into an
// EOS rather than an error.
func serveHLS(t *testing.T, dir string, goodSegments int) string {
	t.Helper()

	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Base(r.URL.Path)
		if !strings.HasSuffix(name, ".m3u8") && int(served.Add(1)) > goodSegments {
			http.Error(w, "upstream unavailable", http.StatusInternalServerError)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, name))
	}))
	t.Cleanup(srv.Close)

	return srv.URL + "/playlist.m3u8"
}

// runHLSPull builds the chain a URL pull builds and runs it until the bus
// reports EOS or an error. The sinks do not sync: for HLS the answer is the
// same either way, because hlsdemux2 leaves segment.stop unset and the sinks
// cannot answer a position query from their own state.
func runHLSPull(t *testing.T, url string) (*Pipeline, gst.MessageType) {
	t.Helper()

	pipeline, err := gst.NewPipelineFromString(fmt.Sprintf(
		"souphttpsrc location=%s ! queue2 use-buffering=true max-size-buffers=0 "+
			"! decodebin3 ! queue ! fakesink sync=false", url))
	require.NoError(t, err)
	t.Cleanup(func() { _ = pipeline.BlockSetState(gst.StateNull) })

	require.NoError(t, pipeline.Start())

	msg := pipeline.GetPipelineBus().TimedPopFiltered(
		gst.ClockTime(60*time.Second), gst.MessageEOS|gst.MessageError)
	require.NotNil(t, msg, "the pull neither finished nor failed")

	return &Pipeline{pipeline: pipeline}, msg.Type()
}

// The reported case, over the topology it was reported on. A 5xx part way
// through has to reach us as an EOS, the duration has to stay the manifest's
// rather than what was delivered, and the position has to be where the
// fragments stopped.
func TestTruncatedHLSPullIsAnError(t *testing.T) {
	dir, advertised := newHLSFixture(t, hlsFixtureSegments)

	p, msgType := runHLSPull(t, serveHLS(t, dir, hlsGoodSegments))

	require.Equal(t, gst.MessageEOS, msgType,
		"a fragment 5xx must arrive as an EOS; a bus error would mean this check is not what catches it")

	ok, d := p.pipeline.QueryDuration(gst.FormatTime)
	require.True(t, ok)
	require.InDelta(t, advertised.Seconds(), time.Duration(d).Seconds(), 0.5,
		"duration must be what the manifest advertises, not what arrived")

	ok, pos := p.pipeline.QueryPosition(gst.FormatTime)
	require.True(t, ok)
	require.InDelta(t, float64(hlsGoodSegments), time.Duration(pos).Seconds(), 1,
		"position must reflect the fragments that arrived")

	require.Error(t, p.checkSourceComplete())
}

// The same topology playing to the end of its playlist has to stay complete,
// which is what keeps the tolerance from failing an ordinary HLS finish.
func TestCompleteHLSPullIsComplete(t *testing.T) {
	dir, advertised := newHLSFixture(t, hlsFixtureSegments)

	p, msgType := runHLSPull(t, serveHLS(t, dir, hlsFixtureSegments+1))

	require.Equal(t, gst.MessageEOS, msgType)

	ok, pos := p.pipeline.QueryPosition(gst.FormatTime)
	require.True(t, ok)
	require.InDelta(t, advertised.Seconds(), time.Duration(pos).Seconds(), 0.5,
		"the whole playlist should have played out")

	require.NoError(t, p.checkSourceComplete())
}
