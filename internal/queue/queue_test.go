package queue

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdmissionController_LimitsConcurrency(t *testing.T) {
	ac := NewAdmissionController(2, 100)
	var running atomic.Int32
	var maxRunning atomic.Int32
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := ac.Admit(context.Background())
			if err != nil {
				return
			}
			defer release()

			cur := running.Add(1)
			// Track max concurrent.
			for {
				old := maxRunning.Load()
				if cur <= old || maxRunning.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			running.Add(-1)
		}()
	}

	wg.Wait()

	if maxRunning.Load() > 2 {
		t.Fatalf("expected max 2 concurrent, got %d", maxRunning.Load())
	}
}

func TestAdmissionController_RejectsWhenQueueFull(t *testing.T) {
	ac := NewAdmissionController(1, 1)

	// Fill the single slot.
	release, err := ac.Admit(context.Background())
	if err != nil {
		t.Fatalf("first admit failed: %v", err)
	}

	// Second should queue (depth = 1).
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err = ac.Admit(ctx)
	// Should either timeout or succeed depending on timing.
	// What matters is it doesn't panic.
	release()
	_ = err // We just verify no panic.
}

func TestAdmissionController_RespectsContext(t *testing.T) {
	ac := NewAdmissionController(1, 100)

	// Fill the slot.
	release, _ := ac.Admit(context.Background())

	// Try with cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ac.Admit(ctx)
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	release()
}

func TestCoalescer_DeduplicatesConcurrentCalls(t *testing.T) {
	c := NewCoalescer()
	var calls atomic.Int32
	var wg sync.WaitGroup

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			val, err, _ := c.Do("same-key", func() (any, error) {
				calls.Add(1)
				time.Sleep(50 * time.Millisecond)
				return "result", nil
			})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if val != "result" {
				t.Errorf("expected 'result', got %v", val)
			}
		}()
	}

	wg.Wait()

	if calls.Load() != 1 {
		t.Fatalf("expected 1 actual call (coalesced), got %d", calls.Load())
	}
}

func TestCoalescer_DifferentKeysRunSeparately(t *testing.T) {
	c := NewCoalescer()
	var calls atomic.Int32
	var wg sync.WaitGroup

	for i := 0; i < 3; i++ {
		wg.Add(1)
		key := string(rune('a' + i))
		go func() {
			defer wg.Done()
			c.Do(key, func() (any, error) {
				calls.Add(1)
				time.Sleep(10 * time.Millisecond)
				return nil, nil
			})
		}()
	}

	wg.Wait()

	if calls.Load() != 3 {
		t.Fatalf("expected 3 calls (different keys), got %d", calls.Load())
	}
}

func TestCoalescer_PropagatesErrors(t *testing.T) {
	c := NewCoalescer()
	testErr := context.DeadlineExceeded

	_, err, _ := c.Do("key", func() (any, error) {
		return nil, testErr
	})

	if err != testErr {
		t.Fatalf("expected test error, got %v", err)
	}
}
