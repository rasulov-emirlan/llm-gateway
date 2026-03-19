// Package apikey manages API keys with tiered rate limits, modeled after
// how OpenAI and Anthropic structure their access tiers. Each key has
// requests-per-minute (RPM), tokens-per-minute (TPM), and daily token caps.
package apikey

import (
	"context"
	"time"
)

// Tier represents an access tier, similar to OpenAI's usage tiers.
type Tier string

const (
	TierFree Tier = "free"
	TierPro  Tier = "pro"
	TierTeam Tier = "team"
)

// APIKey represents a single API key with its metadata and limits.
type APIKey struct {
	Key           string    `json:"key"`
	Name          string    `json:"name"`
	Tier          Tier      `json:"tier"`
	RPM           int       `json:"rpm"`              // requests per minute
	TPM           int       `json:"tpm"`              // tokens per minute
	DailyTokenCap int64    `json:"daily_token_cap"`   // max tokens per day (0 = unlimited)
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
}

// contextKey is unexported to prevent collisions.
type contextKey struct{}

// WithAPIKey stores the API key in the request context.
func WithAPIKey(ctx context.Context, key *APIKey) context.Context {
	return context.WithValue(ctx, contextKey{}, key)
}

// FromContext retrieves the API key from the request context.
// Returns nil if no key is present.
func FromContext(ctx context.Context) *APIKey {
	key, _ := ctx.Value(contextKey{}).(*APIKey)
	return key
}

// DefaultTierLimits returns the default limits for each tier.
func DefaultTierLimits(tier Tier) (rpm, tpm int, dailyCap int64) {
	switch tier {
	case TierFree:
		return 10, 10_000, 100_000
	case TierPro:
		return 60, 100_000, 1_000_000
	case TierTeam:
		return 300, 500_000, 0 // 0 = unlimited
	default:
		return 10, 10_000, 100_000
	}
}
