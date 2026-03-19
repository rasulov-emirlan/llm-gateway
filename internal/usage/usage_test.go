package usage

import (
	"context"
	"testing"
	"time"
)

func TestTracker_RecordAndGetUsage(t *testing.T) {
	tracker := NewTracker()

	tracker.RecordUsage(context.Background(), Record{
		APIKeyID:         "key-1",
		Provider:         "openai",
		Model:            "gpt-4o",
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
	})

	tracker.RecordUsage(context.Background(), Record{
		APIKeyID:         "key-1",
		Provider:         "openai",
		Model:            "gpt-4o-mini",
		PromptTokens:     200,
		CompletionTokens: 100,
		TotalTokens:      300,
	})

	summary := tracker.GetUsage("key-1", time.Now().Add(-1*time.Hour))
	if summary.TotalRequests != 2 {
		t.Fatalf("expected 2 requests, got %d", summary.TotalRequests)
	}
	if summary.TotalTokens != 450 {
		t.Fatalf("expected 450 tokens, got %d", summary.TotalTokens)
	}
	if len(summary.ByModel) != 2 {
		t.Fatalf("expected 2 models, got %d", len(summary.ByModel))
	}
}

func TestTracker_GetUsage_FiltersByKey(t *testing.T) {
	tracker := NewTracker()

	tracker.RecordUsage(context.Background(), Record{
		APIKeyID: "key-1", Model: "gpt-4o", TotalTokens: 100,
	})
	tracker.RecordUsage(context.Background(), Record{
		APIKeyID: "key-2", Model: "gpt-4o", TotalTokens: 200,
	})

	summary := tracker.GetUsage("key-1", time.Now().Add(-1*time.Hour))
	if summary.TotalRequests != 1 {
		t.Fatalf("expected 1 request for key-1, got %d", summary.TotalRequests)
	}
}

func TestTracker_DailyTokens(t *testing.T) {
	tracker := NewTracker()

	tracker.RecordUsage(context.Background(), Record{
		APIKeyID: "key-1", TotalTokens: 500,
	})
	tracker.RecordUsage(context.Background(), Record{
		APIKeyID: "key-1", TotalTokens: 300,
	})

	daily := tracker.DailyTokens("key-1")
	if daily != 800 {
		t.Fatalf("expected 800 daily tokens, got %d", daily)
	}
}

func TestTracker_CheckBudget(t *testing.T) {
	tracker := NewTracker()

	tracker.RecordUsage(context.Background(), Record{
		APIKeyID: "key-1", TotalTokens: 800,
	})

	// With cap of 1000.
	remaining := tracker.CheckBudget("key-1", 1000)
	if remaining != 200 {
		t.Fatalf("expected 200 remaining, got %d", remaining)
	}

	// Over budget.
	tracker.RecordUsage(context.Background(), Record{
		APIKeyID: "key-1", TotalTokens: 300,
	})
	remaining = tracker.CheckBudget("key-1", 1000)
	if remaining != 0 {
		t.Fatalf("expected 0 remaining (over budget), got %d", remaining)
	}
}

func TestTracker_CheckBudget_UnlimitedReturnsNegativeOne(t *testing.T) {
	tracker := NewTracker()
	remaining := tracker.CheckBudget("key-1", 0)
	if remaining != -1 {
		t.Fatalf("expected -1 (unlimited), got %d", remaining)
	}
}

func TestCalculateCost(t *testing.T) {
	// GPT-4o: $2.50/1M input, $10.00/1M output.
	cost := CalculateCost("gpt-4o", 1000, 500)
	expected := 1000.0/1_000_000*2.50 + 500.0/1_000_000*10.00
	if cost != expected {
		t.Fatalf("expected cost %f, got %f", expected, cost)
	}
}

func TestCalculateCost_OllamaIsFree(t *testing.T) {
	cost := CalculateCost("llama3.2", 10000, 5000)
	if cost != 0 {
		t.Fatalf("expected 0 cost for local model, got %f", cost)
	}
}

func TestCalculateCost_UnknownModelIsZero(t *testing.T) {
	cost := CalculateCost("unknown-model", 1000, 500)
	if cost != 0 {
		t.Fatalf("expected 0 for unknown model, got %f", cost)
	}
}
