// Package queue implements admission control, request coalescing, and
// priority-based scheduling. These patterns are used by OpenAI and Anthropic
// to manage load across their infrastructure.
package queue

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
)

// ErrQueueFull is returned when the admission controller's queue is at capacity.
var ErrQueueFull = errors.New("request queue is full, try again later")

// ErrShedding is returned when load shedding is active.
var ErrShedding = errors.New("server is overloaded, request shed")

// AdmissionController limits the number of concurrent requests to providers
// and implements queue depth limits for back-pressure. This is how production
// gateways prevent cascading failures under load.
type AdmissionController struct {
	sem        chan struct{} // semaphore limiting concurrent provider calls
	queueDepth atomic.Int64 // current number of requests waiting
	maxQueue   int64        // reject if queue exceeds this
}

// NewAdmissionController creates an admission controller.
// maxConcurrent: max simultaneous provider calls.
// maxQueueDepth: max requests waiting in queue (0 = unlimited).
func NewAdmissionController(maxConcurrent, maxQueueDepth int) *AdmissionController {
	return &AdmissionController{
		sem:      make(chan struct{}, maxConcurrent),
		maxQueue: int64(maxQueueDepth),
	}
}

// Admit blocks until the request can proceed or the context is cancelled.
// Returns a release function that MUST be called when the request completes.
func (ac *AdmissionController) Admit(ctx context.Context) (release func(), err error) {
	// Check queue depth limit.
	depth := ac.queueDepth.Add(1)
	if ac.maxQueue > 0 && depth > ac.maxQueue {
		ac.queueDepth.Add(-1)
		slog.Warn("admission rejected: queue full",
			"depth", depth,
			"max", ac.maxQueue,
		)
		return nil, ErrQueueFull
	}

	// Wait for a semaphore slot.
	select {
	case ac.sem <- struct{}{}:
		ac.queueDepth.Add(-1)
		return func() { <-ac.sem }, nil
	case <-ctx.Done():
		ac.queueDepth.Add(-1)
		return nil, ctx.Err()
	}
}

// Stats returns current admission controller state.
func (ac *AdmissionController) Stats() (active, queued int) {
	return len(ac.sem), int(ac.queueDepth.Load())
}
