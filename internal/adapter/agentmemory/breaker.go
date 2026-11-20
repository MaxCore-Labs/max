package agentmemory

import (
	"errors"
	"sync"
	"time"

	"max/internal/app"
)

const (
	breakerThreshold = 5
	breakerCooldown  = 60 * time.Second
)

// ErrCircuitOpen is returned when the circuit breaker is open during its 60-second cool-off window.
var ErrCircuitOpen = errors.New("episodic memory circuit breaker open")

// Breaker tracks consecutive failures and opens for 60 seconds after 5 consecutive errors (Spec 8.8.4).
type Breaker struct {
	clock     app.Clock
	mu        sync.Mutex
	failures  int
	openUntil time.Time
}

// NewBreaker constructs a circuit Breaker using clock.
func NewBreaker(clock app.Clock) *Breaker {
	if clock == nil {
		clock = app.SystemClock{}
	}
	return &Breaker{clock: clock}
}

// Healthy reports whether calls are currently permitted through the breaker.
func (b *Breaker) Healthy() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openUntil.IsZero() {
		return true
	}
	return !b.clock.Now().Before(b.openUntil)
}

// Allow checks whether an outbound call may proceed now.
func (b *Breaker) Allow() bool {
	return b.Healthy()
}

// RecordSuccess resets consecutive failure state and closes the circuit.
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	b.failures = 0
	b.openUntil = time.Time{}
	b.mu.Unlock()
}

// RecordFailure increments consecutive failures and opens the circuit for 60s on the 5th failure.
func (b *Breaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.failures >= breakerThreshold {
		b.openUntil = b.clock.Now().Add(breakerCooldown)
	}
}
