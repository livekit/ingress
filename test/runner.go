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

//go:build integration

package test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/rtmp"
	"github.com/livekit/ingress/pkg/service"
	"github.com/livekit/ingress/pkg/utils"
	"github.com/livekit/ingress/pkg/whip"
)

const (
	// How long a case waits for an ingress to reach a status. checkUpdate gives
	// up early on a terminal status it did not ask for, so this only applies to
	// an ingress that stalls.
	stateTimeout = 90 * time.Second
	statePoll    = 100 * time.Millisecond

	idleTimeout          = 30 * time.Second
	serverReadyTimeout   = 30 * time.Second
	serverProbeTimeout   = 250 * time.Millisecond
	monitorWarmup        = 5 * time.Second
	clientTimeout        = 5 * time.Second
	busProbeTimeout      = 2 * time.Second
	publisherStopTimeout = 5 * time.Second

	// How long a publisher streams before the case stops the ingress.
	streamDuration = 45 * time.Second
)

// Runner holds the suite configuration and the state every case shares: one
// service wired the way the server command wires it, the clients that drive it,
// and the ingress states reported back to the fake IOInfo server.
type Runner struct {
	*config.Config `yaml:",inline"`
	RoomName       string `yaml:"room_name"`

	// IntegrationType names the one input type to run, or is empty for all.
	// The INTEGRATION_TYPE variable overrides it, which is how CI selects one.
	IntegrationType string `yaml:"integration_type"`

	svc      *service.Service
	internal rpc.IngressInternalClient
	handler  rpc.IngressHandlerClient
	updates  *stateLog
	infos    *ingressInfos
}

// TestConfig is the name the cloud suite embeds in its own config struct.
type TestConfig = Runner

// stateLog records every state an ingress reports, in order.
//
// The cases ask what an ingress did, not what it is doing: whether it ever
// reached publishing, and what it first ended as. Keeping only the newest state
// answers neither once a session moves on, and a case polling for one can miss
// a status the session passed through between two reads.
//
// Updates arrive on a psrpc handler goroutine, so recording never waits on a
// case: it appends, wakes whatever is waiting, and returns.
type stateLog struct {
	mu     sync.Mutex
	states map[string][]*livekit.IngressState
	// changed is closed and replaced on every record, so any number of waiters
	// can block on one channel and all wake together.
	changed chan struct{}
}

func newStateLog() *stateLog {
	return &stateLog{
		states:  make(map[string][]*livekit.IngressState),
		changed: make(chan struct{}),
	}
}

func (l *stateLog) record(ingressID string, state *livekit.IngressState) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.states[ingressID] = append(l.states[ingressID], state)

	close(l.changed)
	l.changed = make(chan struct{})
}

// scan returns the first recorded state that matches, and a channel that closes
// once something else is recorded.
//
// Both come from one locked section on purpose: taking the channel separately
// would let a record land in between, and the caller would then wait for a
// state it has already been told about.
func (l *stateLog) scan(ingressID string, match func(*livekit.IngressState) bool) (*livekit.IngressState, <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for _, state := range l.states[ingressID] {
		if match(state) {
			return state, l.changed
		}
	}

	return nil, l.changed
}

// history is what the ingress reported, in order, for a message that says what
// happened rather than only what did not.
func (l *stateLog) history(ingressID string) string {
	l.mu.Lock()
	defer l.mu.Unlock()

	states := l.states[ingressID]
	if len(states) == 0 {
		return "nothing"
	}

	reported := make([]string, 0, len(states))
	for _, state := range states {
		reported = append(reported, state.Status.String())
	}

	return strings.Join(reported, ", ")
}

// ingressInfos answers the fake IOInfo server. The service resolves an RTMP or
// WHIP publisher by the stream key it connects with.
type ingressInfos struct {
	mu    sync.Mutex
	byKey map[string]*livekit.IngressInfo
}

func newIngressInfos() *ingressInfos {
	return &ingressInfos{byKey: make(map[string]*livekit.IngressInfo)}
}

func (i *ingressInfos) set(streamKey string, info *livekit.IngressInfo) {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.byKey[streamKey] = info
}

func (i *ingressInfos) remove(streamKey string) {
	i.mu.Lock()
	defer i.mu.Unlock()

	delete(i.byKey, streamKey)
}

func (i *ingressInfos) get(streamKey string) *livekit.IngressInfo {
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.byKey[streamKey]
}

func GetDefaultConfig() *Runner {
	r := &Runner{
		Config: &config.Config{
			ServiceConfig:  &config.ServiceConfig{PSRPC: rpc.DefaultPSRPCConfig},
			InternalConfig: &config.InternalConfig{},
		},
	}

	r.RTMPPort = config.DefaultRTMPPort
	r.HTTPRelayPort = config.DefaultHTTPRelayPort
	r.WHIPPort = config.DefaultWHIPPort

	r.NodeID = "INGRESS_TEST"

	return r
}

func NewRunner(t *testing.T) *Runner {
	r := GetDefaultConfig()

	confString := os.Getenv("INGRESS_CONFIG_BODY")
	if confString == "" {
		confFile := os.Getenv("INGRESS_CONFIG_FILE")
		require.NotEmpty(t, confFile)
		b, err := os.ReadFile(confFile)
		require.NoError(t, err)
		confString = string(b)
	}

	require.NoError(t, yaml.Unmarshal([]byte(confString), r))
	require.NoError(t, r.InitLogger())
	require.NotEmpty(t, r.RoomName, "room_name is required")

	if env := os.Getenv("INTEGRATION_TYPE"); env != "" {
		r.IntegrationType = env
	}
	switch r.IntegrationType {
	case "", "rtmp", "whip", "url":
	default:
		t.Fatalf("integration type %q is not one of rtmp, whip, url", r.IntegrationType)
	}

	return r
}

// StartServer brings up the one service the suite runs against and registers the
// fake IOInfo server the cases read their state updates from.
func (r *Runner) StartServer(
	t *testing.T,
	bus psrpc.MessageBus,
	getStateNotifier func(psrpcClient rpc.IOInfoClient) utils.StateNotifier,
	newCmd func(ctx context.Context, p *params.Params) (*exec.Cmd, error),
) {
	r.requireExclusiveBus(t, bus)

	require.NoError(t, r.RTCConfig.Validate(r.Development))
	r.RTCConfig.EnableLoopbackCandidate = true

	r.updates = newStateLog()
	r.infos = newIngressInfos()

	ios := &ioServer{
		getIngressInfo: func(req *rpc.GetIngressInfoRequest) (*rpc.GetIngressInfoResponse, error) {
			info := r.infos.get(req.StreamKey)
			if info == nil {
				return nil, psrpc.NewErrorf(psrpc.NotFound, "no ingress registered for stream key %q", req.StreamKey)
			}
			return &rpc.GetIngressInfoResponse{Info: info, WsUrl: r.WsUrl}, nil
		},
		updateIngressState: func(req *rpc.UpdateIngressStateRequest) error {
			r.updates.record(req.IngressId, req.State)
			return nil
		},
	}

	ioSrv, err := rpc.NewIOInfoServer(ios, bus)
	require.NoError(t, err)
	t.Cleanup(ioSrv.Kill)

	ioClient, err := rpc.NewIOInfoClient(bus)
	require.NoError(t, err)

	var rtmpSrv *rtmp.RTMPServer
	var whipSrv *whip.WHIPServer
	if r.runRTMP() {
		rtmpSrv = rtmp.NewRTMPServer()
	}
	if r.runWHIP() {
		whipSrv, err = whip.NewWHIPServer(bus)
		require.NoError(t, err)
	}

	svc, err := service.NewService(r.Config, ioClient, getStateNotifier(ioClient), bus, rtmpSrv, whipSrv, newCmd, "")
	require.NoError(t, err)
	svc.StartDebugHandlers()

	if rtmpSrv != nil {
		require.NoError(t, rtmpSrv.Start(r.Config, svc.HandleRTMPPublishRequest))
	}
	if whipSrv != nil {
		require.NoError(t, whipSrv.Start(r.Config, svc.HandleWHIPPublishRequest, svc.GetHealthHandlers()))
	}

	relay := service.NewRelay(rtmpSrv, whipSrv)
	require.NoError(t, relay.Start(r.Config))

	go func() {
		if err := svc.Run(); err != nil {
			t.Errorf("service exited: %v", err)
		}
	}()

	t.Cleanup(func() {
		relay.Stop()
		if rtmpSrv != nil {
			rtmpSrv.Stop()
		}
		if whipSrv != nil {
			whipSrv.Stop()
		}
		svc.Stop(true)
	})

	r.svc = svc

	r.internal, err = rpc.NewIngressInternalClient(bus, psrpc.WithClientTimeout(clientTimeout))
	require.NoError(t, err)
	r.handler, err = rpc.NewIngressHandlerClient(bus, psrpc.WithClientTimeout(clientTimeout))
	require.NoError(t, err)

	r.awaitServer(t)
}

// awaitServer waits until the service is ready to take a case, since neither
// half of that is synchronous with StartServer returning.
func (r *Runner) awaitServer(t *testing.T) {
	t.Helper()

	// The bus is a hard requirement: psrpc subscribes asynchronously, and a
	// request published before the server listens is dropped, not queued.
	require.Eventually(t, r.serverAnswers, serverReadyTimeout, statePoll,
		"the service did not answer on the bus within %s", serverReadyTimeout)

	// The load monitor reports no headroom until its first reading, and an
	// ingress asked for before that is rejected as over capacity.
	//
	// This waits for the reading without insisting on it. CanAccept sizes
	// headroom against the most expensive configured input, while admission
	// charges the one actually requested, so a case on a cheaper input can run
	// while this stays false. It is also the only capacity call that is safe
	// before the monitor starts, as the others read stats that are nil until
	// then.
	for deadline := time.Now().Add(monitorWarmup); time.Now().Before(deadline); {
		if r.svc.CanAccept() {
			return
		}

		time.Sleep(statePoll)
	}
}

// serverAnswers reports whether a request now reaches the service.
//
// ListActiveIngress is read-only and the service registers it from the same Run
// that brings up the rest. No subscriber leaves the response channel to close
// empty once the request times out, so an answer is what proves it is listening.
func (r *Runner) serverAnswers() bool {
	responses, err := r.internal.ListActiveIngress(context.Background(), "",
		&rpc.ListActiveIngressRequest{}, psrpc.WithRequestTimeout(serverProbeTimeout))
	if err != nil {
		return false
	}

	_, ok := <-responses

	return ok
}

// requireExclusiveBus fails unless the suite has the message bus to itself.
// psrpc hands a queue rpc to a single server, so a second IOInfoServer, a
// livekit-server or another ingress, takes a share of the state updates and the
// cases stall with nothing to point at. psrpc rides pub/sub, which a Redis
// database does not scope, so the bus has to be a separate instance and not a
// separate database.
func (r *Runner) requireExclusiveBus(t *testing.T, bus psrpc.MessageBus) {
	probe, err := rpc.NewIOInfoClient(bus, psrpc.WithClientTimeout(busProbeTimeout))
	require.NoError(t, err)
	defer probe.Close()

	_, err = probe.GetIngressInfo(context.Background(), &rpc.GetIngressInfoRequest{
		StreamKey: fmt.Sprintf("bus-probe-%d", time.Now().UnixNano()),
	})

	var psrpcErr psrpc.Error
	if err != nil && errors.As(err, &psrpcErr) {
		switch psrpcErr.Code() {
		case psrpc.Unavailable, psrpc.DeadlineExceeded:
			return
		}
	}

	t.Fatalf("probing IOInfo on the message bus returned %v, expected no response: "+
		"the suite needs a bus of its own, since anything else registering an "+
		"IOInfoServer takes a share of its state updates", err)
}

// runs reports whether an input type is in scope for this run.
func (r *Runner) runs(want string) bool {
	return r.IntegrationType == "" || r.IntegrationType == want
}

func (r *Runner) runRTMP() bool { return r.runs("rtmp") }
func (r *Runner) runWHIP() bool { return r.runs("whip") }
func (r *Runner) runURL() bool  { return r.runs("url") }

// ingressInfo builds the info every case starts from: a unique id and stream
// key, the suite's room, and opus audio.
func (r *Runner) ingressInfo(inputType livekit.IngressInput, name string) *livekit.IngressInfo {
	id := fmt.Sprintf("%s_%d", name, time.Now().UnixNano())

	return &livekit.IngressInfo{
		IngressId:           id,
		InputType:           inputType,
		Name:                name,
		RoomName:            r.RoomName,
		ParticipantIdentity: id,
		ParticipantName:     name,
		Reusable:            true,
		StreamKey:           id,
		Audio: &livekit.IngressAudioOptions{
			Name:   "audio",
			Source: 0,
			EncodingOptions: &livekit.IngressAudioOptions_Options{
				Options: &livekit.IngressAudioEncodingOptions{
					AudioCodec: livekit.AudioCodec_OPUS,
					Bitrate:    64000,
					DisableDtx: false,
					Channels:   2,
				},
			},
		},
	}
}

func videoOptions(layers ...*livekit.VideoLayer) *livekit.IngressVideoOptions {
	return &livekit.IngressVideoOptions{
		Name:   "video",
		Source: 0,
		EncodingOptions: &livekit.IngressVideoOptions_Options{
			Options: &livekit.IngressVideoEncodingOptions{
				VideoCodec: livekit.VideoCodec_H264_BASELINE,
				FrameRate:  20,
				Layers:     layers,
			},
		},
	}
}

func videoLayer(quality livekit.VideoQuality, width, height, bitrate uint32) *livekit.VideoLayer {
	return &livekit.VideoLayer{
		Quality: quality,
		Width:   width,
		Height:  height,
		Bitrate: bitrate,
	}
}

// endMode says what ends a case: the source running out, or a stop we ask for.
type endMode int

const (
	endBySource endMode = iota
	endByDelete
)

// source supplies the media for a case.
//
// The two kinds differ in who connects to whom, which decides the order the
// runner brings things up in: a pull ingress is started against a URL that must
// already serve, while a push ingress has to be resolvable by stream key before
// its publisher connects.
type source interface {
	// pulls reports whether the ingress fetches the media itself.
	pulls() bool
	// url is what the ingress info's Url is set to: the address a pull source
	// serves from, or the endpoint a push source publishes to.
	url(t *testing.T, r *Runner, streamKey string) string
	// publish brings a push source up once the ingress is resolvable, and does
	// nothing for a pull source. It takes the whole info because what a
	// publisher connects to differs by input type: an RTMP url already carries
	// the stream key, while a WHIP publisher appends it to the endpoint.
	publish(t *testing.T, info *livekit.IngressInfo)
}

type testCase struct {
	name      string
	inputType livekit.IngressInput
	source    source
	video     *livekit.IngressVideoOptions
	// reusable decides whether a source that ends is reported as INACTIVE or
	// COMPLETE, so a case that asserts one of those has to set it.
	reusable bool
	// enableTranscoding is a pointer because unset and false are different
	// requests, and only WHIP has a choice to make.
	enableTranscoding *bool

	// A source short enough to reach EOS before ICE completes never reports
	// publishing, so a case on one waits for its terminal status directly.
	skipPublishing bool

	endBy     endMode
	runFor    time.Duration
	expect    livekit.IngressState_Status
	errorLike string
}

// run brings a case up, ends it the way the case asks, and asserts the status
// it ends in.
func (r *Runner) run(t *testing.T, tc *testCase) {
	r.awaitIdle(t)

	t.Run(tc.name, func(t *testing.T) {
		info := r.ingressInfo(tc.inputType, caseIdent(tc.name))
		info.Reusable = tc.reusable
		info.Url = tc.source.url(t, r, info.StreamKey)
		info.EnableTranscoding = tc.enableTranscoding
		if tc.video != nil {
			info.Video = tc.video
		}

		if tc.source.pulls() {
			r.startIngress(t, info)
		} else {
			r.registerIngress(t, info)
			tc.source.publish(t, info)
		}

		if !tc.skipPublishing {
			r.checkUpdate(t, info.IngressId, livekit.IngressState_ENDPOINT_PUBLISHING)
		}

		if tc.endBy == endByDelete {
			time.Sleep(tc.runFor)
			r.stopIngress(t, info.IngressId)
		}

		state := r.awaitTerminal(t, info.IngressId)
		require.Equal(t, tc.expect, state.Status, "ended with: %s", state.Error)
		if tc.errorLike != "" {
			require.Contains(t, state.Error, tc.errorLike)
		}

		r.awaitIdle(t)
	})
}

// caseIdent turns a case name into something usable as a participant identity.
func caseIdent(name string) string {
	return strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(name))
}

// registerIngress makes the info resolvable by stream key, which is how an RTMP
// or WHIP publisher is matched to its ingress when it connects.
func (r *Runner) registerIngress(t *testing.T, info *livekit.IngressInfo) {
	t.Helper()

	r.infos.set(info.StreamKey, info)
	t.Cleanup(func() {
		r.infos.remove(info.StreamKey)
	})
}

// startIngress starts a URL pull and requires the service to report it
// buffering. RTMP and WHIP ingresses start when their publisher connects, so
// they use registerIngress instead.
func (r *Runner) startIngress(t *testing.T, info *livekit.IngressInfo) *livekit.IngressInfo {
	t.Helper()

	logger.Infow("starting url ingress", "ingressID", info.IngressId, "url", info.Url)

	started, err := r.internal.StartIngress(context.Background(), &rpc.StartIngressRequest{Info: info})
	require.NoError(t, err)
	require.Equal(t, livekit.IngressState_ENDPOINT_BUFFERING, started.State.Status)

	return started
}

// checkUpdate waits for the ingress to be reported in the wanted status. A
// terminal status the case did not ask for fails it immediately rather than
// waiting out the timeout.
func (r *Runner) checkUpdate(t *testing.T, ingressID string, want livekit.IngressState_Status) *livekit.IngressState {
	t.Helper()

	return r.await(t, ingressID, want.String(), func(state *livekit.IngressState) bool {
		return state.Status == want
	})
}

// await waits for the first state an ingress reported that matches.
//
// Matching against everything reported rather than the newest state means a
// status the session only held briefly still counts, so a case cannot fail for
// missing something that did happen.
func (r *Runner) await(
	t *testing.T,
	ingressID string,
	want string,
	match func(*livekit.IngressState) bool,
) *livekit.IngressState {
	t.Helper()

	timeout := time.After(stateTimeout)

	for {
		state, changed := r.updates.scan(ingressID, match)
		if state != nil {
			return state
		}

		// A session that has ended reports nothing further, so waiting out the
		// timeout would only make the failure slower and less clear.
		if ended, _ := r.updates.scan(ingressID, endedState); ended != nil {
			t.Fatalf("ingress %s ended as %s without reaching %s: %s",
				ingressID, ended.Status, want, ended.Error)
		}

		select {
		case <-changed:
		case <-timeout:
			t.Fatalf("ingress %s did not reach %s within %s, reported: %s",
				ingressID, want, stateTimeout, r.updates.history(ingressID))
		}
	}
}

// awaitTerminal waits for the ingress to stop, whatever the outcome. Cases that
// end by source use it, since a source driven end is classified and the status
// it lands on is the thing under test.
func (r *Runner) awaitTerminal(t *testing.T, ingressID string) *livekit.IngressState {
	t.Helper()

	return r.await(t, ingressID, "a terminal status", endedState)
}

// stopIngress deletes the ingress and returns the state it ended in. The caller
// asserts which terminal status it expects, since that differs by input type.
func (r *Runner) stopIngress(t *testing.T, ingressID string) *livekit.IngressState {
	t.Helper()

	_, err := r.handler.DeleteIngress(context.Background(), ingressID, &livekit.DeleteIngressRequest{
		IngressId: ingressID,
	})
	require.NoError(t, err)

	return r.awaitTerminal(t, ingressID)
}

// awaitIdle waits for the service to release every session, so the next case
// starts against a service with nothing still running on it.
func (r *Runner) awaitIdle(t *testing.T) {
	t.Helper()

	deadline := time.Now().Add(idleTimeout)
	for time.Now().Before(deadline) {
		if len(r.svc.ListIngress()) == 0 {
			return
		}
		time.Sleep(time.Second)
	}

	t.Fatalf("service still holds %d session(s) after %s", len(r.svc.ListIngress()), idleTimeout)
}

// publish runs a publisher for the rest of the case. One left running would
// reconnect against the next case's ingress.
func publish(t *testing.T, command string) {
	t.Helper()

	logger.Infow("starting publisher", "command", command)

	args := strings.Fields(command)
	cmd := exec.Command(args[0], args[1:]...)
	require.NoError(t, cmd.Start())

	t.Cleanup(func() {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			return
		}

		exited := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(exited)
		}()

		// SIGKILL cannot be ignored, so the second wait always returns.
		select {
		case <-exited:
		case <-time.After(publisherStopTimeout):
			_ = cmd.Process.Kill()
			<-exited
		}
	})
}

func endedState(state *livekit.IngressState) bool {
	return isTerminal(state.Status)
}

func isTerminal(status livekit.IngressState_Status) bool {
	switch status {
	case livekit.IngressState_ENDPOINT_COMPLETE,
		livekit.IngressState_ENDPOINT_INACTIVE,
		livekit.IngressState_ENDPOINT_ERROR:
		return true
	}
	return false
}
