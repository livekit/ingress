package service

import (
	"sync"

	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

type SessionType string

const (
	SessionType_HandlerProcess SessionType = "HandlerProcess"
	SessionType_Service                    = "Service"
)

type SessionManager struct {
	monitor *stats.Monitor

	lock     sync.Mutex
	sessions map[string]SessionType
}

func NewSessionManager(monitor *stats.Monitor) *SessionManager {
	return &SessionManager{
		monitor:  monitor,
		sessions: make(map[string]SessionType),
	}
}

func (sm *SessionManager) IngressStarted(info *livekit.IngressInfo, t SessionType) {
	logger.Infow("ingress started", "ingressID", info.IngressId, "type", t)

	sm.lock.Lock()
	defer sm.lock.Unlock()

	sm.sessions[info.IngressId] = t

	sm.monitor.IngressStarted(info)
}

func (sm *SessionManager) IngressEnded(info *livekit.IngressInfo) {
	logger.Infow("ingress ended", "ingressID", info.IngressId)

	sm.lock.Lock()
	defer sm.lock.Unlock()

	delete(sm.sessions, info.IngressId)

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
	for ingressID, _ := range sm.sessions {
		ingressIDs = append(ingressIDs, ingressID)
	}
	return ingressIDs
}
