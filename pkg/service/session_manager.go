package service

import (
	"sync"

	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

type SessionType string

type SessionManager struct {
	monitor *stats.Monitor

	lock     sync.Mutex
	sessions map[string]*livekit.IngressInfo // resourceId -> IngressInfo
}

func NewSessionManager(monitor *stats.Monitor) *SessionManager {
	return &SessionManager{
		monitor:  monitor,
		sessions: make(map[string]*livekit.IngressInfo),
	}
}

func (sm *SessionManager) IngressStarted(info *livekit.IngressInfo) {
	logger.Infow("ingress started", "ingressID", info.IngressId, "resourceID", info.State.ResourceId)

	sm.lock.Lock()
	defer sm.lock.Unlock()

	sm.sessions[info.State.ResourceId] = info

	sm.monitor.IngressStarted(info)
}

func (sm *SessionManager) IngressEnded(info *livekit.IngressInfo) {
	logger.Infow("ingress ended", "ingressID", info.IngressId, "resourceID", info.State.ResourceId)

	sm.lock.Lock()
	defer sm.lock.Unlock()

	delete(sm.sessions, info.State.ResourceId)

	sm.monitor.IngressEnded(info)
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
	for _, info := range sm.sessions {
		ingressIDs = append(ingressIDs, info.IngressId)
	}
	return ingressIDs
}
