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
	"sync"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

type sessionRecord struct {
	info               *livekit.IngressInfo
	sessionAPI         types.SessionAPI
	mediaStats         *stats.MediaStatsReporter
	localStatsGatherer *stats.LocalMediaStatsGatherer
}

type SessionManager struct {
	monitor *stats.Monitor

	lock     sync.Mutex
	sessions map[string]*sessionRecord // resourceId -> sessionRecord
}

func NewSessionManager(monitor *stats.Monitor) *SessionManager {
	return &SessionManager{
		monitor:  monitor,
		sessions: make(map[string]*sessionRecord),
	}
}

func (sm *SessionManager) IngressStarted(info *livekit.IngressInfo, sessionAPI types.SessionAPI) {
	logger.Infow("ingress started", "ingressID", info.IngressId, "resourceID", info.State.ResourceId)

	sm.lock.Lock()
	defer sm.lock.Unlock()

	r := &sessionRecord{
		info:               info,
		sessionAPI:         sessionAPI,
		mediaStats:         stats.NewMediaStats(sessionAPI),
		localStatsGatherer: stats.NewLocalMediaStatsGatherer(),
	}
	r.mediaStats.RegisterGatherer(r.localStatsGatherer)

	sm.sessions[info.State.ResourceId] = r

	sm.monitor.IngressStarted(info)
}

func (sm *SessionManager) IngressEnded(info *livekit.IngressInfo) {
	logger.Infow("ingress ended", "ingressID", info.IngressId, "resourceID", info.State.ResourceId)

	sm.lock.Lock()
	defer sm.lock.Unlock()

	p := sm.sessions[info.State.ResourceId]
	if p != nil {
		delete(sm.sessions, info.State.ResourceId)
		p.mediaStats.Close()
	}

	sm.monitor.IngressEnded(info)
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

	return record.mediaStats, nil
}

func (sm *SessionManager) IsIdle() bool {
	sm.lock.Lock()
	defer sm.lock.Unlock()

	return len(sm.sessions) == 0
}

func (sm *SessionManager) ListIngress() []string {
	sm.lock.Lock()
	defer sm.lock.Unlock()

	ingressIDs := make([]string, 0, len(sm.sessions))
	for _, r := range sm.sessions {
		ingressIDs = append(ingressIDs, r.info.IngressId)
	}
	return ingressIDs
}
