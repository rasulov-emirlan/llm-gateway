package usage

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Record represents a single usage event.
type Record struct {
	APIKeyID         string
	Provider         string
	Model            string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CostUSD          float64
	Timestamp        time.Time
}

// Summary aggregates usage over a time period.
type Summary struct {
	TotalRequests    int64              `json:"total_requests"`
	TotalTokens      int64              `json:"total_tokens"`
	PromptTokens     int64              `json:"prompt_tokens"`
	CompletionTokens int64              `json:"completion_tokens"`
	TotalCostUSD     float64            `json:"total_cost_usd"`
	ByModel          map[string]*ModelUsage `json:"by_model"`
}

// ModelUsage tracks per-model usage.
type ModelUsage struct {
	Requests         int64   `json:"requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

// Tracker records and queries usage data. In-memory implementation
// suitable for single-instance deployments; can be extended with
// Redis or Postgres backing for production.
type Tracker struct {
	mu      sync.RWMutex
	records []Record

	// dailyTokens tracks tokens consumed per key per day for budget enforcement.
	dailyTokens map[string]*dailyCounter
}

type dailyCounter struct {
	date   string // YYYY-MM-DD
	tokens int64
}

// NewTracker creates a new in-memory usage tracker.
func NewTracker() *Tracker {
	return &Tracker{
		records:     make([]Record, 0, 1024),
		dailyTokens: make(map[string]*dailyCounter),
	}
}

// RecordUsage records a usage event and updates daily counters.
func (t *Tracker) RecordUsage(_ context.Context, rec Record) {
	rec.Timestamp = time.Now()
	rec.CostUSD = CalculateCost(rec.Model, rec.PromptTokens, rec.CompletionTokens)

	t.mu.Lock()
	defer t.mu.Unlock()

	t.records = append(t.records, rec)

	// Update daily token counter.
	today := time.Now().Format("2006-01-02")
	dc, ok := t.dailyTokens[rec.APIKeyID]
	if !ok || dc.date != today {
		dc = &dailyCounter{date: today, tokens: 0}
		t.dailyTokens[rec.APIKeyID] = dc
	}
	dc.tokens += int64(rec.TotalTokens)

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
func (t *Tracker) DailyTokens(keyID string) int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	today := time.Now().Format("2006-01-02")
	dc, ok := t.dailyTokens[keyID]
	if !ok || dc.date != today {
		return 0
	}
	return dc.tokens
}

// CheckBudget returns the remaining daily tokens for a key.
// Returns -1 if unlimited (cap == 0).
func (t *Tracker) CheckBudget(keyID string, dailyCap int64) int64 {
	if dailyCap == 0 {
		return -1 // unlimited
	}
	used := t.DailyTokens(keyID)
	remaining := dailyCap - used
	if remaining < 0 {
		return 0
	}
	return remaining
}

// GetUsage returns a usage summary for a key within the given time window.
func (t *Tracker) GetUsage(keyID string, since time.Time) *Summary {
	t.mu.RLock()
	defer t.mu.RUnlock()

	summary := &Summary{
		ByModel: make(map[string]*ModelUsage),
	}

	for _, rec := range t.records {
		if rec.APIKeyID != keyID || rec.Timestamp.Before(since) {
			continue
		}

		summary.TotalRequests++
		summary.TotalTokens += int64(rec.TotalTokens)
		summary.PromptTokens += int64(rec.PromptTokens)
		summary.CompletionTokens += int64(rec.CompletionTokens)
		summary.TotalCostUSD += rec.CostUSD

		mu, ok := summary.ByModel[rec.Model]
		if !ok {
			mu = &ModelUsage{}
			summary.ByModel[rec.Model] = mu
		}
		mu.Requests++
		mu.PromptTokens += int64(rec.PromptTokens)
		mu.CompletionTokens += int64(rec.CompletionTokens)
		mu.TotalTokens += int64(rec.TotalTokens)
		mu.CostUSD += rec.CostUSD
	}

	return summary
}
