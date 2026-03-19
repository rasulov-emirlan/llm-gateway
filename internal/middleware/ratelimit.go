package middleware

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/erasulov/llm-gateway/internal/telemetry"
)

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64
	burst   int
	once    sync.Once
	metrics *telemetry.Metrics
}

// RateLimit returns middleware that enforces a per-client token bucket rate limit.
// rps is the refill rate (tokens per second), burst is the maximum bucket size.
func RateLimit(rps float64, burst int, metrics *telemetry.Metrics) func(http.Handler) http.Handler {
	rl := &rateLimiter{
		buckets: make(map[string]*bucket),
		rate:    rps,
		burst:   burst,
		metrics: metrics,
	}

	return func(next http.Handler) http.Handler {
		rl.once.Do(func() {
			go rl.cleanup(30 * time.Second)
		})

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := clientIP(r)
			if !rl.allow(key) {
				rl.metrics.RateLimited.Add(r.Context(), 1, telemetry.WithAttr(telemetry.ClientAttr(key)))
				w.Header().Set("Retry-After", "1")
				http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, exists := rl.buckets[key]
	if !exists {
		rl.buckets[key] = &bucket{
			tokens:   float64(rl.burst) - 1,
			lastSeen: now,
		}
		return true
	}

	// Refill tokens based on elapsed time.
	elapsed := now.Sub(b.lastSeen).Seconds()
	b.tokens += elapsed * rl.rate
	if b.tokens > float64(rl.burst) {
		b.tokens = float64(rl.burst)
	}
	b.lastSeen = now

	if b.tokens < 1 {
		return false
	}

	b.tokens--
	return true
}

// cleanup removes stale buckets periodically to prevent memory leaks.
func (rl *rateLimiter) cleanup(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		rl.mu.Lock()
		cutoff := time.Now().Add(-2 * time.Minute)
		for key, b := range rl.buckets {
			if b.lastSeen.Before(cutoff) {
				delete(rl.buckets, key)
			}
		}
		rl.mu.Unlock()
	}
}

func clientIP(r *http.Request) string {
	// Check X-Forwarded-For for reverse proxy setups.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for j := 0; j < len(xff); j++ {
			if xff[j] == ',' {
				return xff[:j]
			}
		}
		return xff
	}

	if xff := r.Header.Get("X-Real-IP"); xff != "" {
		return xff
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
