package daemon

import (
	"sync"
	"time"
)

// idleTimer shuts the daemon down after a quiet period. Every request
// resets it; a step in flight holds it (Hold/Release) so a long assertion
// can't be cut off by the timer.
type idleTimer struct {
	mu       sync.Mutex
	timeout  time.Duration
	deadline time.Time
	holds    int
	timer    *time.Timer
	fire     func()
	stopped  bool
}

func newIdleTimer(timeout time.Duration, fire func()) *idleTimer {
	t := &idleTimer{timeout: timeout, fire: fire}
	t.Touch()
	return t
}

// Touch resets the countdown.
func (t *idleTimer) Touch() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resetLocked()
}

func (t *idleTimer) resetLocked() {
	if t.timeout <= 0 || t.stopped {
		return
	}
	t.deadline = time.Now().Add(t.timeout)
	if t.timer != nil {
		t.timer.Stop()
	}
	t.timer = time.AfterFunc(t.timeout, t.check)
}

// check fires the shutdown unless something touched the timer or holds it.
func (t *idleTimer) check() {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return
	}
	if t.holds > 0 || time.Now().Before(t.deadline) {
		t.resetLocked()
		t.mu.Unlock()
		return
	}
	t.stopped = true
	t.mu.Unlock()
	if t.fire != nil {
		t.fire()
	}
}

// Hold pauses idle shutdown until the matching Release.
func (t *idleTimer) Hold() {
	t.mu.Lock()
	t.holds++
	t.mu.Unlock()
}

// Release ends a Hold and restarts the countdown.
func (t *idleTimer) Release() {
	t.mu.Lock()
	if t.holds > 0 {
		t.holds--
	}
	t.resetLocked()
	t.mu.Unlock()
}

// Deadline is the current idle deadline (zero when disabled).
func (t *idleTimer) Deadline() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.timeout <= 0 {
		return time.Time{}
	}
	return t.deadline
}

// Stop disables the timer for good.
func (t *idleTimer) Stop() {
	t.mu.Lock()
	t.stopped = true
	if t.timer != nil {
		t.timer.Stop()
	}
	t.mu.Unlock()
}
