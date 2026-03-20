package redisclient

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// New creates a shared Redis client from the given URL.
// Returns nil, nil if the URL is empty (Redis disabled).
func New(redisURL string) (*redis.Client, error) {
	if redisURL == "" {
		slog.Info("redis disabled (no url configured)")
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
		client.Close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}

	slog.Info("redis connected", "url", redisURL)
	return client, nil
}
