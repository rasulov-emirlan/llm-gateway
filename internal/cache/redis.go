package cache

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/erasulov/llm-gateway/internal/provider"
	"github.com/redis/go-redis/v9"
)

type Cache struct {
	client *redis.Client
	ttl    time.Duration
}

// New creates a Redis-backed cache. If redisURL is empty, returns nil (disabled).
func New(redisURL string, ttl time.Duration) (*Cache, error) {
	if redisURL == "" {
		slog.Info("cache disabled (no redis url configured)")
		return nil, nil
	}

	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}

	client := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping: %w", err)
	}

	slog.Info("cache enabled", "redis_url", redisURL, "ttl", ttl)
	return &Cache{client: client, ttl: ttl}, nil
}

// Get retrieves a cached response. Returns nil, false if cache is disabled or key not found.
func (c *Cache) Get(ctx context.Context, key string) (*provider.ChatResponse, bool) {
	if c == nil {
		return nil, false
	}

	data, err := c.client.Get(ctx, key).Bytes()
	if err != nil {
		return nil, false
	}

	var resp provider.ChatResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, false
	}

	return &resp, true
}

// Set stores a response in cache. No-op if cache is disabled.
func (c *Cache) Set(ctx context.Context, key string, resp *provider.ChatResponse) {
	if c == nil {
		return
	}

	data, err := json.Marshal(resp)
	if err != nil {
		slog.Error("cache marshal error", "error", err)
		return
	}

	if err := c.client.Set(ctx, key, data, c.ttl).Err(); err != nil {
		slog.Error("cache set error", "error", err)
	}
}

// Key computes a cache key from the model and messages.
func Key(model string, messages []provider.Message) string {
	payload := struct {
		Model    string             `json:"model"`
		Messages []provider.Message `json:"messages"`
	}{
		Model:    model,
		Messages: messages,
	}

	data, _ := json.Marshal(payload)
	hash := sha256.Sum256(data)
	return fmt.Sprintf("llmcache:%x", hash)
}

// Close shuts down the Redis client. No-op if cache is disabled.
func (c *Cache) Close() error {
	if c == nil {
		return nil
	}
	return c.client.Close()
}
