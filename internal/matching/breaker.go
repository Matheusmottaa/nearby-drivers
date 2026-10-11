package matching

import (
	"sync"
	"time"
)

const (
	breakerFailures = 5
	breakerCooldown = 10 * time.Second
)

const (
	breakerClosed = iota
	breakerOpen
	breakerHalf
)

// Breaker opens after breakerFailures consecutive failures and allows one
// probe after breakerCooldown. An open breaker is a signal to skip the call.
type Breaker struct {
	mu        sync.Mutex
	state     int
	failures  int
	openUntil time.Time
	now       func() time.Time
}

// NewBreaker returns a closed breaker. now defaults to time.Now.
func NewBreaker(now func() time.Time) *Breaker {
	if now == nil {
		now = time.Now
	}
	return &Breaker{now: now}
}

// Allow reports whether a call may proceed. A half-open breaker admits one probe.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case breakerClosed:
		return true
	case breakerOpen:
		if b.now().Before(b.openUntil) {
			return false
		}
		b.state = breakerHalf
		return true
	default:
		return false
	}
}

// Success closes the breaker.
func (b *Breaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.state = breakerClosed
}

// Failure counts a consecutive error. The fifth opens the breaker.
func (b *Breaker) Failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.state == breakerHalf || b.failures >= breakerFailures {
		b.failures = breakerFailures
		b.state = breakerOpen
		b.openUntil = b.now().Add(breakerCooldown)
	}
}
