package media

import "sync"

// eosDispatcher broadcasts a single EOS signal to any listener that registers,
// replaying the signal to late subscribers if it already fired.
type eosDispatcher struct {
	mu        sync.Mutex
	fired     bool
	listeners []func()
}

func newEOSDispatcher() *eosDispatcher {
	return &eosDispatcher{}
}

func (d *eosDispatcher) Fire() {
	d.mu.Lock()
	if d.fired {
		d.mu.Unlock()
		return
	}
	d.fired = true
	listeners := append([]func(){}, d.listeners...)
	d.listeners = nil
	d.mu.Unlock()

	for _, l := range listeners {
		go l()
	}
}

func (d *eosDispatcher) AddListener(f func()) {
	d.mu.Lock()
	if d.fired {
		d.mu.Unlock()
		// Replay immediately if EOS already fired
		go f()
		return
	}
	d.listeners = append(d.listeners, f)
	d.mu.Unlock()
}
