package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// slidingWindowLua atomically prunes, counts, and optionally adds to a sorted set.
// KEYS[1] = rate limit key
// ARGV[1] = window start (oldest allowed timestamp in nanoseconds)
// ARGV[2] = now (nanoseconds, used as score)
// ARGV[3] = unique member ID
// ARGV[4] = max allowed count
// ARGV[5] = window TTL in milliseconds
// Returns: [current_count, allowed (1/0)]
var slidingWindowLua = redis.NewScript(`
local key = KEYS[1]
local window_start = tonumber(ARGV[1])
local now = tonumber(ARGV[2])
local member = ARGV[3]
local limit = tonumber(ARGV[4])
local ttl_ms = tonumber(ARGV[5])

-- Prune entries outside the window.
redis.call('ZREMRANGEBYSCORE', key, '-inf', window_start)

-- Count remaining entries.
local count = redis.call('ZCARD', key)

if count < limit then
    -- Under limit: record this request.
    redis.call('ZADD', key, now, member)
    redis.call('PEXPIRE', key, ttl_ms)
    return {count + 1, 1}
end

return {count, 0}
`)

// redisSlidingWindow implements a distributed sliding window using Redis sorted sets.
type redisSlidingWindow struct {
	client *redis.Client
}

// result holds the outcome of a rate limit check.
type slidingWindowResult struct {
	count   int
	allowed bool
}

// check performs an atomic sliding window rate limit check.
func (r *redisSlidingWindow) check(ctx context.Context, key string, limit int, window time.Duration) (slidingWindowResult, error) {
	now := time.Now().UnixNano()
	windowStart := now - window.Nanoseconds()
	member := uniqueMember(now)
	ttlMs := window.Milliseconds() * 2 // Key expires after 2x window for safety.

	res, err := slidingWindowLua.Run(ctx, r.client, []string{key},
		windowStart, now, member, limit, ttlMs,
	).Int64Slice()
	if err != nil {
		return slidingWindowResult{}, fmt.Errorf("redis rate limit: %w", err)
	}

	return slidingWindowResult{
		count:   int(res[0]),
		allowed: res[1] == 1,
	}, nil
}

// uniqueMember generates a unique sorted set member to avoid dedup.
func uniqueMember(nowNano int64) string {
	var buf [8]byte
	rand.Read(buf[:])
	return fmt.Sprintf("%d:%s", nowNano, hex.EncodeToString(buf[:]))
}
