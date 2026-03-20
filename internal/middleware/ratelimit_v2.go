package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/erasulov/llm-gateway/internal/apikey"
	"github.com/erasulov/llm-gateway/internal/telemetry"
	"github.com/redis/go-redis/v9"
)

// slidingWindow tracks request timestamps within a rolling window.
// This implements a sliding window log algorithm, which is how Anthropic
// and OpenAI enforce per-minute rate limits.
type slidingWindow struct {
	mu         sync.Mutex
	timestamps []time.Time
}

// count returns the number of events in the window and prunes old entries.
func (sw *slidingWindow) count(window time.Duration) int {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	cutoff := time.Now().Add(-window)

	// Prune expired entries.
	i := 0
	for i < len(sw.timestamps) && sw.timestamps[i].Before(cutoff) {
		i++
	}
	sw.timestamps = sw.timestamps[i:]

	return len(sw.timestamps)
}

// add records a new event.
func (sw *slidingWindow) add() {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.timestamps = append(sw.timestamps, time.Now())
}

// rateLimiterV2 implements per-API-key sliding window rate limiting.
type rateLimiterV2 struct {
	mu      sync.Mutex
	windows map[string]*slidingWindow // key → sliding window
	metrics *telemetry.Metrics

	// Redis-backed distributed rate limiting (nil = in-memory fallback).
	redis *redisSlidingWindow
}

// RateLimitV2 returns middleware that enforces per-API-key sliding window
// rate limiting. It reads the API key from context (set by Auth middleware)
// and enforces RPM limits. If no API key is present, it falls back to
// per-IP rate limiting with the provided default RPM.
//
// If redisClient is non-nil, rate limits are enforced via Redis (distributed).
// Otherwise, falls back to in-memory enforcement (single-instance only).
func RateLimitV2(defaultRPM int, metrics *telemetry.Metrics, redisClient *redis.Client) func(http.Handler) http.Handler {
	rl := &rateLimiterV2{
		windows: make(map[string]*slidingWindow),
		metrics: metrics,
	}

	if redisClient != nil {
		rl.redis = &redisSlidingWindow{client: redisClient}
		slog.Info("rate limiter using redis (distributed mode)")
	} else {
		slog.Info("rate limiter using in-memory (single-instance mode)")
	}

	// Background cleanup of stale in-memory windows.
	if redisClient == nil {
		go rl.cleanup(5 * time.Minute)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, rpm := rl.resolveKeyAndLimit(r, defaultRPM)

			count, allowed, err := rl.checkLimit(r.Context(), key, rpm)
			if err != nil {
				// Redis error: log and allow (fail-open).
				slog.Error("rate limit check failed, allowing request", "error", err, "key", key)
				next.ServeHTTP(w, r)
				return
			}

			if !allowed {
				rl.metrics.RateLimited.Add(r.Context(), 1,
					telemetry.WithAttr(telemetry.ClientAttr(key)))

				slog.Warn("rate limit exceeded",
					"key", key,
					"rpm", rpm,
					"current", count,
				)

				w.Header().Set("Retry-After", "60")
				w.Header().Set("X-RateLimit-Limit", formatInt(rpm))
				w.Header().Set("X-RateLimit-Remaining", "0")
				http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
				return
			}

			// Set rate limit headers (like OpenAI does).
			w.Header().Set("X-RateLimit-Limit", formatInt(rpm))
			w.Header().Set("X-RateLimit-Remaining", formatInt(rpm-count))

			next.ServeHTTP(w, r)
		})
	}
}

// checkLimit checks the rate limit using Redis or in-memory fallback.
// Returns (current count after this request, allowed, error).
func (rl *rateLimiterV2) checkLimit(ctx context.Context, key string, rpm int) (int, bool, error) {
	if rl.redis != nil {
		redisKey := "ratelimit:" + key
		res, err := rl.redis.check(ctx, redisKey, rpm, time.Minute)
		if err != nil {
			return 0, false, err
		}
		return res.count, res.allowed, nil
	}

	// In-memory fallback.
	window := rl.getWindow(key)
	count := window.count(time.Minute)

	if count >= rpm {
		return count, false, nil
	}

	window.add()
	return count + 1, true, nil
}

// resolveKeyAndLimit determines the rate limit key and RPM for the request.
func (rl *rateLimiterV2) resolveKeyAndLimit(r *http.Request, defaultRPM int) (string, int) {
	if k := apikey.FromContext(r.Context()); k != nil {
		return "key:" + k.Key[:8], k.RPM // Use first 8 chars as key ID for logs
	}
	return "ip:" + clientIP(r), defaultRPM
}

// getWindow returns the sliding window for a key, creating one if needed.
func (rl *rateLimiterV2) getWindow(key string) *slidingWindow {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	w, ok := rl.windows[key]
	if !ok {
		w = &slidingWindow{}
		rl.windows[key] = w
	}
	return w
}

// cleanup removes windows with no recent activity.
func (rl *rateLimiterV2) cleanup(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		rl.mu.Lock()
		for key, w := range rl.windows {
			if w.count(2*time.Minute) == 0 {
				delete(rl.windows, key)
			}
		}
		rl.mu.Unlock()
	}
}

func formatInt(n int) string {
	// Simple int to string without importing strconv.
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 10)
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	// Reverse.
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
