package service

import (
	"sync"

	"github.com/livekit/ingress/pkg/stats"
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

func (sm *SessionManager) IngressStarted(ingressID string, t SessionType) {
	logger.Infow("ingress started", "ingressID", ingressID, "type", t)

	sm.lock.Lock()
	defer sm.lock.Unlock()

	sm.sessions[ingressID] = t
}

func (sm *SessionManager) IngressEnded(ingressID string) {
	logger.Infow("ingress ended", "ingressID", ingressID)

	sm.lock.Lock()
	defer sm.lock.Unlock()

	delete(sm.sessions, ingressID)
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
