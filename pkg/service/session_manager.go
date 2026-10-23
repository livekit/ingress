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

package service

import (
	"context"
	"os"
	"sync"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/types"
)

type sessionRecord struct {
	info               *livekit.IngressInfo
	sessionAPI         types.SessionAPI
	mediaStats         *stats.MediaStatsReporter
	localStatsGatherer *stats.LocalMediaStatsGatherer
}

// endedTracked is how many recently ended resource ids are kept so that a start
// still in flight can be matched to its own end. They are released together
// rather than one by one, so the number held sits between endedTracked and twice
// that.
const endedTracked = 4096

type SessionManager struct {
	monitor *stats.Monitor
	rpcSrv  rpc.IngressInternalServer

	lock       sync.Mutex
	sessions   map[string]*sessionRecord // resourceId -> sessionRecord
	ended      map[string]struct{}       // resourceIds whose end arrived first
	endedOlder map[string]struct{}       // the ids held before ended started filling
}

func NewSessionManager(monitor *stats.Monitor, rpcSrv rpc.IngressInternalServer) *SessionManager {
	return &SessionManager{
		monitor:    monitor,
		rpcSrv:     rpcSrv,
		sessions:   make(map[string]*sessionRecord),
		ended:      make(map[string]struct{}),
		endedOlder: make(map[string]struct{}),
	}
}

func (sm *SessionManager) IngressStarted(info *livekit.IngressInfo, sessionAPI types.SessionAPI) {
	if sm.registerSession(info, sessionAPI) {
		logger.Infow("ingress started", "ingressID", info.IngressId, "resourceID", info.State.ResourceId)
		return
	}

	logger.Infow("ignoring start of a session that already ended",
		"ingressID", info.IngressId, "resourceID", info.State.ResourceId)
}

// registerSession records the session unless its end has already been seen, and
// reports whether it was recorded.
func (sm *SessionManager) registerSession(info *livekit.IngressInfo, sessionAPI types.SessionAPI) bool {
	sm.lock.Lock()
	defer sm.lock.Unlock()

	if sm.takeEnded(info.State.ResourceId) {
		return false
	}

	r := &sessionRecord{
		info:               info,
		sessionAPI:         sessionAPI,
		mediaStats:         stats.NewMediaStats(sessionAPI, livekit.IngressInput_name[int32(info.InputType)]),
		localStatsGatherer: stats.NewLocalMediaStatsGatherer(),
	}
	r.mediaStats.RegisterGatherer(r.localStatsGatherer)
	// Register remote gatherer, if any
	r.mediaStats.RegisterGatherer(sessionAPI)

	sm.sessions[info.State.ResourceId] = r

	sm.monitor.IngressStarted(info)

	return true
}

// HandlerStarted attributes the handler process pid to the session in info.
func (sm *SessionManager) HandlerStarted(info *livekit.IngressInfo, pid int) {
	sm.monitor.HandlerStarted(info, pid)
}

// HandlerExited records the run of the exited handler process pid.
func (sm *SessionManager) HandlerExited(pid int, state *os.ProcessState) {
	sm.monitor.HandlerExited(pid, state)
}

func (sm *SessionManager) IngressEnded(resourceID string) {
	if u := sm.monitor.SessionEnded(resourceID); u != nil {
		logger.Infow("ingress metrics",
			"ingressID", u.IngressID,
			"resourceID", resourceID,
			"inputType", u.InputType.String(),
			"transcoding", u.Transcoding,
			"avgCPU", u.AvgCPU(),
			"maxCPU", u.MaxCPU,
			"maxMemory", u.MaxMemory,
			"cpuSeconds", u.CPUTime.Seconds(),
			"durationSeconds", u.Duration.Seconds(),
		)
	}

	sm.lock.Lock()
	defer sm.lock.Unlock()

	p := sm.sessions[resourceID]
	if p == nil {
		// A session can be ended before its start has been recorded, so the id
		// is held until that start discards itself. Dropping this leaves the
		// late start registered with nothing left to end it.
		sm.recordEnded(resourceID)
		return
	}

	logger.Infow("ingress ended", "ingressID", p.info.IngressId, "resourceID", resourceID)

	delete(sm.sessions, p.info.State.ResourceId)
	p.sessionAPI.CloseSession(context.Background())
	p.mediaStats.Close()
	sm.monitor.IngressEnded(p.info)
}

// recordEnded notes a resource id that ended without a session, so that a start
// still in flight can discard itself. The caller holds sm.lock.
func (sm *SessionManager) recordEnded(resourceID string) {
	// Moved aside rather than released, so an id is kept until at least
	// endedTracked further ids have been recorded.
	if len(sm.ended) >= endedTracked {
		sm.endedOlder = sm.ended
		sm.ended = make(map[string]struct{}, endedTracked)
	}

	sm.ended[resourceID] = struct{}{}
}

// takeEnded removes resourceID from the ids held by recordEnded and reports
// whether it was there. The caller holds sm.lock.
func (sm *SessionManager) takeEnded(resourceID string) bool {
	if _, ok := sm.ended[resourceID]; ok {
		delete(sm.ended, resourceID)
		return true
	}

	if _, ok := sm.endedOlder[resourceID]; ok {
		delete(sm.endedOlder, resourceID)
		return true
	}

	return false
}

func (sm *SessionManager) GetIngressSessionAPI(resourceId string) (types.SessionAPI, error) {
	sm.lock.Lock()
	defer sm.lock.Unlock()

	record, ok := sm.sessions[resourceId]
	if !ok {
		return nil, errors.ErrIngressNotFound
	}

	return record.sessionAPI, nil
}

func (sm *SessionManager) GetIngressMediaStats(resourceId string) (*stats.LocalMediaStatsGatherer, error) {
	sm.lock.Lock()
	defer sm.lock.Unlock()

	record, ok := sm.sessions[resourceId]
	if !ok {
		return nil, errors.ErrIngressNotFound
	}

	return record.localStatsGatherer, nil
}

func (sm *SessionManager) IsIdle() bool {
	sm.lock.Lock()
	defer sm.lock.Unlock()

	return len(sm.sessions) == 0
}

func (sm *SessionManager) ListIngress() []*rpc.IngressSession {
	sm.lock.Lock()
	defer sm.lock.Unlock()

	ingressIDs := make([]*rpc.IngressSession, 0, len(sm.sessions))
	for _, r := range sm.sessions {
		var resourceID string
		if r.info.State != nil {
			resourceID = r.info.State.ResourceId
		}
		ingressIDs = append(ingressIDs, &rpc.IngressSession{IngressId: r.info.IngressId, ResourceId: resourceID})
	}
	return ingressIDs
}
