package middleware

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/erasulov/llm-gateway/internal/apikey"
	"github.com/erasulov/llm-gateway/internal/telemetry"
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
}

// RateLimitV2 returns middleware that enforces per-API-key sliding window
// rate limiting. It reads the API key from context (set by Auth middleware)
// and enforces RPM limits. If no API key is present, it falls back to
// per-IP rate limiting with the provided default RPM.
func RateLimitV2(defaultRPM int, metrics *telemetry.Metrics) func(http.Handler) http.Handler {
	rl := &rateLimiterV2{
		windows: make(map[string]*slidingWindow),
		metrics: metrics,
	}

	// Background cleanup of stale windows.
	go rl.cleanup(5 * time.Minute)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, rpm := rl.resolveKeyAndLimit(r, defaultRPM)

			window := rl.getWindow(key)
			count := window.count(time.Minute)

			if count >= rpm {
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

			window.add()

			// Set rate limit headers (like OpenAI does).
			w.Header().Set("X-RateLimit-Limit", formatInt(rpm))
			w.Header().Set("X-RateLimit-Remaining", formatInt(rpm-count-1))

			next.ServeHTTP(w, r)
		})
	}
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
