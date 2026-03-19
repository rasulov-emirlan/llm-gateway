package resilience

import (
	"context"
	"errors"
	"testing"
	"time"
)

var errTest = errors.New("test error")

func TestCircuitBreaker_StartsInClosedState(t *testing.T) {
	cb := NewCircuitBreaker("test", DefaultCircuitBreakerConfig())
	if cb.State() != StateClosed {
		t.Fatalf("expected closed, got %s", cb.State())
	}
}

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	cfg := CircuitBreakerConfig{
		Threshold:   3,
		Timeout:     1 * time.Second,
		HalfOpenMax: 2,
	}
	cb := NewCircuitBreaker("test", cfg)
	ctx := context.Background()

	// 3 failures should trip the circuit open.
	for i := 0; i < 3; i++ {
		cb.Execute(ctx, func(ctx context.Context) error {
			return errTest
		})
	}

	if cb.State() != StateOpen {
		t.Fatalf("expected open after %d failures, got %s", cfg.Threshold, cb.State())
	}
}

func TestCircuitBreaker_RejectsWhenOpen(t *testing.T) {
	cfg := CircuitBreakerConfig{
		Threshold:   1,
		Timeout:     10 * time.Second, // long timeout so it stays open
		HalfOpenMax: 1,
	}
	cb := NewCircuitBreaker("test", cfg)
	ctx := context.Background()

	// Trip it open.
	cb.Execute(ctx, func(ctx context.Context) error { return errTest })

	// Should be rejected.
	err := cb.Execute(ctx, func(ctx context.Context) error { return nil })
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}
}

func TestCircuitBreaker_TransitionsToHalfOpenAfterTimeout(t *testing.T) {
	cfg := CircuitBreakerConfig{
		Threshold:   1,
		Timeout:     50 * time.Millisecond,
		HalfOpenMax: 1,
	}
	cb := NewCircuitBreaker("test", cfg)
	ctx := context.Background()

	// Trip it open.
	cb.Execute(ctx, func(ctx context.Context) error { return errTest })
	if cb.State() != StateOpen {
		t.Fatalf("expected open, got %s", cb.State())
	}

	// Wait for timeout.
	time.Sleep(60 * time.Millisecond)

	// Should transition to half-open on next state check.
	if cb.State() != StateHalfOpen {
		t.Fatalf("expected half-open after timeout, got %s", cb.State())
	}
}

func TestCircuitBreaker_ClosesAfterHalfOpenSuccesses(t *testing.T) {
	cfg := CircuitBreakerConfig{
		Threshold:   1,
		Timeout:     50 * time.Millisecond,
		HalfOpenMax: 2,
	}
	cb := NewCircuitBreaker("test", cfg)
	ctx := context.Background()

	// Trip it open.
	cb.Execute(ctx, func(ctx context.Context) error { return errTest })

	// Wait for half-open.
	time.Sleep(60 * time.Millisecond)

	// Two successes should close it.
	for i := 0; i < 2; i++ {
		err := cb.Execute(ctx, func(ctx context.Context) error { return nil })
		if err != nil {
			t.Fatalf("unexpected error on success %d: %v", i, err)
		}
	}

	if cb.State() != StateClosed {
		t.Fatalf("expected closed after %d half-open successes, got %s", cfg.HalfOpenMax, cb.State())
	}
}

func TestCircuitBreaker_HalfOpenFailureReopens(t *testing.T) {
	cfg := CircuitBreakerConfig{
		Threshold:   1,
		Timeout:     50 * time.Millisecond,
		HalfOpenMax: 3,
	}
	cb := NewCircuitBreaker("test", cfg)
	ctx := context.Background()

	// Trip open → wait → half-open.
	cb.Execute(ctx, func(ctx context.Context) error { return errTest })
	time.Sleep(60 * time.Millisecond)

	// Fail in half-open → back to open.
	cb.Execute(ctx, func(ctx context.Context) error { return errTest })

	if cb.State() != StateOpen {
		t.Fatalf("expected open after half-open failure, got %s", cb.State())
	}
}

func TestCircuitBreaker_SuccessResetsFailureCount(t *testing.T) {
	cfg := CircuitBreakerConfig{
		Threshold:   3,
		Timeout:     1 * time.Second,
		HalfOpenMax: 1,
	}
	cb := NewCircuitBreaker("test", cfg)
	ctx := context.Background()

	// 2 failures, then a success, then 2 more failures.
	cb.Execute(ctx, func(ctx context.Context) error { return errTest })
	cb.Execute(ctx, func(ctx context.Context) error { return errTest })
	cb.Execute(ctx, func(ctx context.Context) error { return nil }) // resets
	cb.Execute(ctx, func(ctx context.Context) error { return errTest })
	cb.Execute(ctx, func(ctx context.Context) error { return errTest })

	// Should still be closed (never hit 3 consecutive).
	if cb.State() != StateClosed {
		t.Fatalf("expected closed (success reset), got %s", cb.State())
	}
}
