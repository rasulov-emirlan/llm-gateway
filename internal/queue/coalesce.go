package queue

import (
	"log/slog"
	"sync"
)

// Coalescer deduplicates identical in-flight requests using the singleflight
// pattern. If request A is in-flight for a given cache key and request B
// arrives with the same key, B waits for A's response instead of making
// a duplicate provider call. This reduces load on providers and improves
// latency for duplicate requests.
type Coalescer struct {
	mu    sync.Mutex
	calls map[string]*call
}

// call represents an in-flight operation.
type call struct {
	wg  sync.WaitGroup
	val any
	err error
}

// NewCoalescer creates a new request coalescer.
func NewCoalescer() *Coalescer {
	return &Coalescer{
		calls: make(map[string]*call),
	}
}

// Do executes fn for the given key, deduplicating concurrent calls with
// the same key. Returns (result, error, shared) where shared indicates
// if the result was shared with another caller.
func (c *Coalescer) Do(key string, fn func() (any, error)) (any, error, bool) {
	c.mu.Lock()
	if call, ok := c.calls[key]; ok {
		c.mu.Unlock()
		call.wg.Wait()
		slog.Debug("coalesced request", "key", key[:16])
		return call.val, call.err, true
	}

	call := &call{}
	call.wg.Add(1)
	c.calls[key] = call
	c.mu.Unlock()

	call.val, call.err = fn()
	call.wg.Done()

	c.mu.Lock()
	delete(c.calls, key)
	c.mu.Unlock()

	return call.val, call.err, false
}

// InFlight returns the number of unique in-flight operations.
func (c *Coalescer) InFlight() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}
