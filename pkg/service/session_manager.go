package service

import (
	"sync"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

type sessionRecord struct {
	info       *livekit.IngressInfo
	sessionAPI SessionAPI
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

func (sm *SessionManager) IngressStarted(info *livekit.IngressInfo, sessionAPI SessionAPI) {
	logger.Infow("ingress started", "ingressID", info.IngressId, "resourceID", info.State.ResourceId)

	sm.lock.Lock()
	defer sm.lock.Unlock()

	sm.sessions[info.State.ResourceId] = &sessionRecord{
		info:       info,
		sessionAPI: sessionAPI,
	}

	sm.monitor.IngressStarted(info)
}

func (sm *SessionManager) IngressEnded(info *livekit.IngressInfo) {
	logger.Infow("ingress ended", "ingressID", info.IngressId, "resourceID", info.State.ResourceId)

	sm.lock.Lock()
	defer sm.lock.Unlock()

	delete(sm.sessions, info.State.ResourceId)

	sm.monitor.IngressEnded(info)
}

func (sm *SessionManager) GetIngressSessionAPI(resourceId string) (SessionAPI, error) {
	sm.lock.Lock()
	defer sm.lock.Unlock()

	record, ok := sm.sessions[resourceId]
	if !ok {
		return nil, errors.ErrIngressNotFound
	}

	return record.sessionAPI, nil
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
