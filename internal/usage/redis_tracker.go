package usage

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// Hash TTL — 48 hours covers today + yesterday for cross-midnight queries.
	hashTTL = 48 * time.Hour
)

// RedisTracker is a Redis-backed usage tracker for multi-instance deployments.
// It stores daily aggregates per API key using Redis hashes.
//
// Key schema:
//
//	usage:daily:{keyID}:{YYYY-MM-DD}              → hash of aggregate counters
//	usage:daily:{keyID}:{YYYY-MM-DD}:model:{model} → hash of per-model counters
//
// Hash fields: requests, prompt_tokens, completion_tokens, total_tokens, cost_usd_micro
// (cost is stored as micro-dollars to avoid floating point in Redis HINCRBY)
type RedisTracker struct {
	client *redis.Client
}

// NewRedisTracker creates a Redis-backed usage tracker.
func NewRedisTracker(client *redis.Client) *RedisTracker {
	return &RedisTracker{client: client}
}

// RecordUsage records a usage event by incrementing Redis hash counters.
func (t *RedisTracker) RecordUsage(ctx context.Context, rec Record) {
	rec.Timestamp = time.Now()
	rec.CostUSD = CalculateCost(rec.Model, rec.PromptTokens, rec.CompletionTokens)

	today := time.Now().Format("2006-01-02")
	dailyKey := fmt.Sprintf("usage:daily:%s:%s", rec.APIKeyID, today)
	modelKey := fmt.Sprintf("usage:daily:%s:%s:model:%s", rec.APIKeyID, today, rec.Model)
	costMicro := int64(math.Round(rec.CostUSD * 1_000_000))

	pipe := t.client.Pipeline()

	// Increment daily aggregate.
	pipe.HIncrBy(ctx, dailyKey, "requests", 1)
	pipe.HIncrBy(ctx, dailyKey, "prompt_tokens", int64(rec.PromptTokens))
	pipe.HIncrBy(ctx, dailyKey, "completion_tokens", int64(rec.CompletionTokens))
	pipe.HIncrBy(ctx, dailyKey, "total_tokens", int64(rec.TotalTokens))
	pipe.HIncrBy(ctx, dailyKey, "cost_usd_micro", costMicro)
	pipe.Expire(ctx, dailyKey, hashTTL)

	// Increment per-model aggregate.
	pipe.HIncrBy(ctx, modelKey, "requests", 1)
	pipe.HIncrBy(ctx, modelKey, "prompt_tokens", int64(rec.PromptTokens))
	pipe.HIncrBy(ctx, modelKey, "completion_tokens", int64(rec.CompletionTokens))
	pipe.HIncrBy(ctx, modelKey, "total_tokens", int64(rec.TotalTokens))
	pipe.HIncrBy(ctx, modelKey, "cost_usd_micro", costMicro)
	pipe.Expire(ctx, modelKey, hashTTL)

	if _, err := pipe.Exec(ctx); err != nil {
		slog.Error("redis usage record failed", "error", err, "key", rec.APIKeyID)
		return
	}

	if rec.CostUSD > 0 {
		slog.Debug("usage recorded",
			"key", rec.APIKeyID,
			"model", rec.Model,
			"tokens", rec.TotalTokens,
			"cost_usd", rec.CostUSD,
		)
	}
}

// DailyTokens returns the total tokens consumed by a key today.
func (t *RedisTracker) DailyTokens(keyID string) int64 {
	ctx := context.Background()
	today := time.Now().Format("2006-01-02")
	dailyKey := fmt.Sprintf("usage:daily:%s:%s", keyID, today)

	val, err := t.client.HGet(ctx, dailyKey, "total_tokens").Int64()
	if err != nil {
		return 0
	}
	return val
}

// CheckBudget returns the remaining daily tokens for a key.
// Returns -1 if unlimited (cap == 0).
func (t *RedisTracker) CheckBudget(keyID string, dailyCap int64) int64 {
	if dailyCap == 0 {
		return -1
	}
	used := t.DailyTokens(keyID)
	remaining := dailyCap - used
	if remaining < 0 {
		return 0
	}
	return remaining
}

// GetUsage returns a usage summary for a key within the given time window.
// It scans daily hashes for each date in the range.
func (t *RedisTracker) GetUsage(keyID string, since time.Time) *Summary {
	ctx := context.Background()
	summary := &Summary{
		ByModel: make(map[string]*ModelUsage),
	}

	// Iterate over each day in the range.
	now := time.Now()
	for d := since.Truncate(24 * time.Hour); !d.After(now); d = d.Add(24 * time.Hour) {
		date := d.Format("2006-01-02")
		dailyKey := fmt.Sprintf("usage:daily:%s:%s", keyID, date)

		vals, err := t.client.HGetAll(ctx, dailyKey).Result()
		if err != nil || len(vals) == 0 {
			continue
		}

		summary.TotalRequests += parseInt64(vals["requests"])
		summary.PromptTokens += parseInt64(vals["prompt_tokens"])
		summary.CompletionTokens += parseInt64(vals["completion_tokens"])
		summary.TotalTokens += parseInt64(vals["total_tokens"])
		summary.TotalCostUSD += float64(parseInt64(vals["cost_usd_micro"])) / 1_000_000

		// Scan for per-model keys for this day.
		t.scanModelUsage(ctx, keyID, date, summary)
	}

	return summary
}

// scanModelUsage populates per-model breakdown from Redis.
func (t *RedisTracker) scanModelUsage(ctx context.Context, keyID, date string, summary *Summary) {
	pattern := fmt.Sprintf("usage:daily:%s:%s:model:*", keyID, date)
	prefix := fmt.Sprintf("usage:daily:%s:%s:model:", keyID, date)

	var cursor uint64
	for {
		keys, next, err := t.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			slog.Error("redis scan model keys failed", "error", err)
			return
		}

		for _, key := range keys {
			model := key[len(prefix):]
			vals, err := t.client.HGetAll(ctx, key).Result()
			if err != nil || len(vals) == 0 {
				continue
			}

			mu, ok := summary.ByModel[model]
			if !ok {
				mu = &ModelUsage{}
				summary.ByModel[model] = mu
			}
			mu.Requests += parseInt64(vals["requests"])
			mu.PromptTokens += parseInt64(vals["prompt_tokens"])
			mu.CompletionTokens += parseInt64(vals["completion_tokens"])
			mu.TotalTokens += parseInt64(vals["total_tokens"])
			mu.CostUSD += float64(parseInt64(vals["cost_usd_micro"])) / 1_000_000
		}

		cursor = next
		if cursor == 0 {
			break
		}
	}
}

func parseInt64(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}
