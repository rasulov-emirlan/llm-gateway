package middleware

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setupRedisSlidingWindow(t *testing.T) (*redisSlidingWindow, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return &redisSlidingWindow{client: client}, mr
}

func TestRedisSlidingWindow_AllowsUnderLimit(t *testing.T) {
	sw, _ := setupRedisSlidingWindow(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		res, err := sw.check(ctx, "ratelimit:test", 10, time.Minute)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.allowed {
			t.Fatalf("request %d should be allowed", i)
		}
		if res.count != i+1 {
			t.Fatalf("expected count %d, got %d", i+1, res.count)
		}
	}
}

func TestRedisSlidingWindow_BlocksOverLimit(t *testing.T) {
	sw, _ := setupRedisSlidingWindow(t)
	ctx := context.Background()

	limit := 3
	for i := 0; i < limit; i++ {
		res, err := sw.check(ctx, "ratelimit:test", limit, time.Minute)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.allowed {
			t.Fatalf("request %d should be allowed", i)
		}
	}

	// Next request should be blocked.
	res, err := sw.check(ctx, "ratelimit:test", limit, time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.allowed {
		t.Fatal("request should be blocked (over limit)")
	}
	if res.count != limit {
		t.Fatalf("expected count %d, got %d", limit, res.count)
	}
}

func TestRedisSlidingWindow_ExpiredEntriesPruned(t *testing.T) {
	sw, mr := setupRedisSlidingWindow(t)
	ctx := context.Background()

	// Fill to limit.
	limit := 3
	for i := 0; i < limit; i++ {
		sw.check(ctx, "ratelimit:test", limit, time.Minute)
	}

	// Fast-forward past the window.
	mr.FastForward(2 * time.Minute)

	// Should be allowed again after window expires.
	res, err := sw.check(ctx, "ratelimit:test", limit, time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.allowed {
		t.Fatal("request should be allowed after window expires")
	}
	if res.count != 1 {
		t.Fatalf("expected count 1 after expiry, got %d", res.count)
	}
}

func TestRedisSlidingWindow_DifferentKeysIndependent(t *testing.T) {
	sw, _ := setupRedisSlidingWindow(t)
	ctx := context.Background()

	// Fill key-a to limit.
	limit := 2
	for i := 0; i < limit; i++ {
		sw.check(ctx, "ratelimit:key-a", limit, time.Minute)
	}

	// key-b should still be allowed.
	res, err := sw.check(ctx, "ratelimit:key-b", limit, time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.allowed {
		t.Fatal("key-b should be allowed (independent from key-a)")
	}
}
