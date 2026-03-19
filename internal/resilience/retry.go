package resilience

import (
	"context"
	"math"
	"math/rand/v2"
	"time"
)

// RetryConfig configures the retry behavior.
type RetryConfig struct {
	// MaxAttempts is the total number of attempts (1 = no retries).
	MaxAttempts int

	// InitialDelay is the delay before the first retry.
	InitialDelay time.Duration

	// MaxDelay caps the exponential backoff delay.
	MaxDelay time.Duration

	// Multiplier is the exponential backoff multiplier (typically 2.0).
	Multiplier float64

	// JitterFactor adds randomness to prevent thundering herd (0.0-1.0).
	// A value of 0.5 means the delay can vary by ±50%.
	JitterFactor float64

	// Retryable determines if an error should be retried. If nil, all errors are retried.
	Retryable func(error) bool
}

// DefaultRetryConfig returns sensible defaults for LLM provider calls.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:  3,
		InitialDelay: 500 * time.Millisecond,
		MaxDelay:     10 * time.Second,
		Multiplier:   2.0,
		JitterFactor: 0.5,
	}
}

// Retry executes fn with exponential backoff and jitter.
// It respects context cancellation and the Retryable predicate.
func Retry(ctx context.Context, cfg RetryConfig, fn func(ctx context.Context) error) error {
	var lastErr error

	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		lastErr = fn(ctx)
		if lastErr == nil {
			return nil
		}

		// Check if this error is retryable.
		if cfg.Retryable != nil && !cfg.Retryable(lastErr) {
			return lastErr
		}

		// Don't sleep after the last attempt.
		if attempt == cfg.MaxAttempts-1 {
			break
		}

		delay := computeDelay(attempt, cfg)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}

	return lastErr
}

// computeDelay calculates the backoff delay with jitter for a given attempt.
//
// Formula: base = initialDelay * multiplier^attempt
// Then apply "full jitter": delay = random(base * (1-jitter), base * (1+jitter))
// This is the recommended approach from AWS's "Exponential Backoff And Jitter" paper.
func computeDelay(attempt int, cfg RetryConfig) time.Duration {
	base := float64(cfg.InitialDelay) * math.Pow(cfg.Multiplier, float64(attempt))
	if base > float64(cfg.MaxDelay) {
		base = float64(cfg.MaxDelay)
	}

	if cfg.JitterFactor <= 0 {
		return time.Duration(base)
	}

	// Full jitter: uniform random between base*(1-jitter) and base*(1+jitter).
	low := base * (1 - cfg.JitterFactor)
	high := base * (1 + cfg.JitterFactor)
	jittered := low + rand.Float64()*(high-low)

	return time.Duration(jittered)
}
