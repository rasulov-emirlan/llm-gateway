package resilience

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetry_SucceedsOnFirstAttempt(t *testing.T) {
	calls := 0
	err := Retry(context.Background(), DefaultRetryConfig(), func(ctx context.Context) error {
		calls++
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestRetry_RetriesOnFailure(t *testing.T) {
	cfg := RetryConfig{
		MaxAttempts:  3,
		InitialDelay: 1 * time.Millisecond,
		MaxDelay:     10 * time.Millisecond,
		Multiplier:   2.0,
		JitterFactor: 0,
	}

	calls := 0
	err := Retry(context.Background(), cfg, func(ctx context.Context) error {
		calls++
		if calls < 3 {
			return errTest
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestRetry_ReturnsLastError(t *testing.T) {
	cfg := RetryConfig{
		MaxAttempts:  2,
		InitialDelay: 1 * time.Millisecond,
		MaxDelay:     10 * time.Millisecond,
		Multiplier:   2.0,
		JitterFactor: 0,
	}

	err := Retry(context.Background(), cfg, func(ctx context.Context) error {
		return errTest
	})

	if !errors.Is(err, errTest) {
		t.Fatalf("expected errTest, got %v", err)
	}
}

func TestRetry_RespectsContext(t *testing.T) {
	cfg := RetryConfig{
		MaxAttempts:  10,
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     1 * time.Second,
		Multiplier:   2.0,
		JitterFactor: 0,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	calls := 0
	err := Retry(ctx, cfg, func(ctx context.Context) error {
		calls++
		return errTest
	})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline, got %v", err)
	}
	// Should have made at least 1 call but not all 10.
	if calls >= 10 {
		t.Fatalf("expected fewer than 10 calls, got %d", calls)
	}
}

func TestRetry_SkipsNonRetryableErrors(t *testing.T) {
	permanentErr := errors.New("permanent error")
	cfg := RetryConfig{
		MaxAttempts:  5,
		InitialDelay: 1 * time.Millisecond,
		MaxDelay:     10 * time.Millisecond,
		Multiplier:   2.0,
		JitterFactor: 0,
		Retryable: func(err error) bool {
			return !errors.Is(err, permanentErr)
		},
	}

	calls := 0
	err := Retry(context.Background(), cfg, func(ctx context.Context) error {
		calls++
		return permanentErr
	})

	if !errors.Is(err, permanentErr) {
		t.Fatalf("expected permanent error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call (non-retryable), got %d", calls)
	}
}

func TestComputeDelay_ExponentialBackoff(t *testing.T) {
	cfg := RetryConfig{
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     10 * time.Second,
		Multiplier:   2.0,
		JitterFactor: 0, // no jitter for deterministic test
	}

	delays := []time.Duration{
		computeDelay(0, cfg),
		computeDelay(1, cfg),
		computeDelay(2, cfg),
		computeDelay(3, cfg),
	}

	expected := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
	}

	for i, d := range delays {
		if d != expected[i] {
			t.Errorf("attempt %d: expected %v, got %v", i, expected[i], d)
		}
	}
}

func TestComputeDelay_CapsAtMaxDelay(t *testing.T) {
	cfg := RetryConfig{
		InitialDelay: 1 * time.Second,
		MaxDelay:     5 * time.Second,
		Multiplier:   10.0,
		JitterFactor: 0,
	}

	delay := computeDelay(5, cfg) // 1s * 10^5 = 100000s, capped to 5s
	if delay != 5*time.Second {
		t.Fatalf("expected max delay 5s, got %v", delay)
	}
}
