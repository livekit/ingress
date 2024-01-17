package stats

import "time"

type trackStats struct {
	totalBytes int64
	startTime  time.Time

	currentBytes  int64
	lastQueryTime time.Time
}

func (s *trackStats) mediaReceived(size int64) {
	if s.startTime.IsZero() {
		now := time.Now()

		s.startTime = now
		s.lastQueryTime = now
	}

	s.totalBytes += size
	s.currentBytes += size
}

func (s *trackStats) getStats() (uint32, uint32) {
	now := time.Now()

	averageBps := uint32(float64(s.totalBytes) * 8 * float64(time.Second) / float64(now.Sub(s.startTime)))
	currentBps := uint32(float64(s.currentBytes) * 8 * float64(time.Second) / float64(now.Sub(s.lastQueryTime)))

	s.lastQueryTime = now
	s.currentBytes = 0

	return averageBps, currentBps
}
