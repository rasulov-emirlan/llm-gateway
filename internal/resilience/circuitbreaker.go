// Package resilience implements circuit breaker and retry patterns for
// provider calls. These are core distributed systems resilience patterns
// used by every serious infrastructure team.
//
// The circuit breaker implements a three-state machine:
//
//	Closed  → normal operation, requests flow through
//	Open    → provider is failing, requests are rejected immediately
//	HalfOpen → probing recovery, limited requests allowed through
//
// State transitions:
//
//	Closed  → Open:     after `threshold` consecutive failures
//	Open    → HalfOpen: after `timeout` elapses
//	HalfOpen → Closed:  after `halfOpenMax` consecutive successes
//	HalfOpen → Open:    on any failure during probing
package resilience

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// State represents the circuit breaker state.
type State int

const (
	StateClosed   State = iota // Normal operation
	StateOpen                  // Rejecting requests
	StateHalfOpen              // Probing recovery
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// ErrCircuitOpen is returned when the circuit breaker is open and not
// accepting requests.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// CircuitBreakerConfig configures a circuit breaker instance.
type CircuitBreakerConfig struct {
	// Threshold is the number of consecutive failures before tripping open.
	Threshold int

	// Timeout is how long to stay in Open state before transitioning to HalfOpen.
	Timeout time.Duration

	// HalfOpenMax is the number of consecutive successes needed in HalfOpen
	// to transition back to Closed.
	HalfOpenMax int
}

// DefaultCircuitBreakerConfig returns sensible defaults.
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		Threshold:   5,
		Timeout:     30 * time.Second,
		HalfOpenMax: 3,
	}
}

// CircuitBreaker implements the circuit breaker pattern for a single provider.
type CircuitBreaker struct {
	mu sync.Mutex

	name            string
	state           State
	failures        int
	successes       int
	lastStateChange time.Time
	cfg             CircuitBreakerConfig
}

// NewCircuitBreaker creates a circuit breaker for the named provider.
func NewCircuitBreaker(name string, cfg CircuitBreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{
		name:            name,
		state:           StateClosed,
		lastStateChange: time.Now(),
		cfg:             cfg,
	}
}

// State returns the current circuit breaker state.
func (cb *CircuitBreaker) State() State {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	// Check if we should transition from Open → HalfOpen.
	if cb.state == StateOpen && time.Since(cb.lastStateChange) >= cb.cfg.Timeout {
		cb.transitionTo(StateHalfOpen)
	}

	return cb.state
}

// Execute runs fn if the circuit allows it. It records success or failure
// and manages state transitions accordingly.
func (cb *CircuitBreaker) Execute(ctx context.Context, fn func(ctx context.Context) error) error {
	if !cb.allowRequest() {
		return fmt.Errorf("%s: %w", cb.name, ErrCircuitOpen)
	}

	err := fn(ctx)

	if err != nil {
		cb.recordFailure()
	} else {
		cb.recordSuccess()
	}

	return err
}

// allowRequest checks if a request should be allowed through.
func (cb *CircuitBreaker) allowRequest() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		// Check if timeout has elapsed → transition to HalfOpen.
		if time.Since(cb.lastStateChange) >= cb.cfg.Timeout {
			cb.transitionTo(StateHalfOpen)
			return true
		}
		return false
	case StateHalfOpen:
		return true
	default:
		return false
	}
}

// recordSuccess records a successful call.
func (cb *CircuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures = 0

	switch cb.state {
	case StateHalfOpen:
		cb.successes++
		if cb.successes >= cb.cfg.HalfOpenMax {
			cb.transitionTo(StateClosed)
		}
	case StateClosed:
		// Already closed, nothing to do.
	}
}

// recordFailure records a failed call.
func (cb *CircuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.successes = 0

	switch cb.state {
	case StateClosed:
		cb.failures++
		if cb.failures >= cb.cfg.Threshold {
			cb.transitionTo(StateOpen)
		}
	case StateHalfOpen:
		// Any failure in HalfOpen → back to Open.
		cb.transitionTo(StateOpen)
	}
}

// transitionTo changes state. Must be called with mu held.
func (cb *CircuitBreaker) transitionTo(newState State) {
	oldState := cb.state
	cb.state = newState
	cb.failures = 0
	cb.successes = 0
	cb.lastStateChange = time.Now()

	slog.Info("circuit breaker state change",
		"provider", cb.name,
		"from", oldState.String(),
		"to", newState.String(),
	)
}
