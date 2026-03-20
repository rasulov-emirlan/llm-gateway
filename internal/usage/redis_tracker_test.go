package usage

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setupRedisTracker(t *testing.T) (*RedisTracker, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return NewRedisTracker(client), mr
}

func TestRedisTracker_RecordAndDailyTokens(t *testing.T) {
	tracker, _ := setupRedisTracker(t)

	tracker.RecordUsage(context.Background(), Record{
		APIKeyID: "key-1", Model: "gpt-4o", TotalTokens: 500, PromptTokens: 300, CompletionTokens: 200,
	})
	tracker.RecordUsage(context.Background(), Record{
		APIKeyID: "key-1", Model: "gpt-4o", TotalTokens: 300, PromptTokens: 200, CompletionTokens: 100,
	})

	daily := tracker.DailyTokens("key-1")
	if daily != 800 {
		t.Fatalf("expected 800 daily tokens, got %d", daily)
	}
}

func TestRedisTracker_DailyTokens_DifferentKeys(t *testing.T) {
	tracker, _ := setupRedisTracker(t)

	tracker.RecordUsage(context.Background(), Record{
		APIKeyID: "key-1", TotalTokens: 500,
	})
	tracker.RecordUsage(context.Background(), Record{
		APIKeyID: "key-2", TotalTokens: 300,
	})

	if d := tracker.DailyTokens("key-1"); d != 500 {
		t.Fatalf("key-1: expected 500, got %d", d)
	}
	if d := tracker.DailyTokens("key-2"); d != 300 {
		t.Fatalf("key-2: expected 300, got %d", d)
	}
}

func TestRedisTracker_CheckBudget(t *testing.T) {
	tracker, _ := setupRedisTracker(t)

	tracker.RecordUsage(context.Background(), Record{
		APIKeyID: "key-1", TotalTokens: 800,
	})

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

func TestRedisTracker_CheckBudget_Unlimited(t *testing.T) {
	tracker, _ := setupRedisTracker(t)
	remaining := tracker.CheckBudget("key-1", 0)
	if remaining != -1 {
		t.Fatalf("expected -1 (unlimited), got %d", remaining)
	}
}

func TestRedisTracker_GetUsage(t *testing.T) {
	tracker, _ := setupRedisTracker(t)

	tracker.RecordUsage(context.Background(), Record{
		APIKeyID: "key-1", Provider: "openai", Model: "gpt-4o",
		PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150,
	})
	tracker.RecordUsage(context.Background(), Record{
		APIKeyID: "key-1", Provider: "openai", Model: "gpt-4o-mini",
		PromptTokens: 200, CompletionTokens: 100, TotalTokens: 300,
	})

	summary := tracker.GetUsage("key-1", time.Now().Add(-1*time.Hour))
	if summary.TotalRequests != 2 {
		t.Fatalf("expected 2 requests, got %d", summary.TotalRequests)
	}
	if summary.TotalTokens != 450 {
		t.Fatalf("expected 450 tokens, got %d", summary.TotalTokens)
	}
	if summary.PromptTokens != 300 {
		t.Fatalf("expected 300 prompt tokens, got %d", summary.PromptTokens)
	}
	if summary.CompletionTokens != 150 {
		t.Fatalf("expected 150 completion tokens, got %d", summary.CompletionTokens)
	}
}

func TestRedisTracker_GetUsage_ByModel(t *testing.T) {
	tracker, _ := setupRedisTracker(t)

	tracker.RecordUsage(context.Background(), Record{
		APIKeyID: "key-1", Model: "gpt-4o",
		PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150,
	})
	tracker.RecordUsage(context.Background(), Record{
		APIKeyID: "key-1", Model: "gpt-4o-mini",
		PromptTokens: 200, CompletionTokens: 100, TotalTokens: 300,
	})

	summary := tracker.GetUsage("key-1", time.Now().Add(-1*time.Hour))
	if len(summary.ByModel) != 2 {
		t.Fatalf("expected 2 models, got %d", len(summary.ByModel))
	}

	gpt4o := summary.ByModel["gpt-4o"]
	if gpt4o == nil {
		t.Fatal("missing gpt-4o model usage")
	}
	if gpt4o.TotalTokens != 150 {
		t.Fatalf("gpt-4o: expected 150 tokens, got %d", gpt4o.TotalTokens)
	}
	if gpt4o.Requests != 1 {
		t.Fatalf("gpt-4o: expected 1 request, got %d", gpt4o.Requests)
	}
}

func TestRedisTracker_GetUsage_FiltersByKey(t *testing.T) {
	tracker, _ := setupRedisTracker(t)

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
	if summary.TotalTokens != 100 {
		t.Fatalf("expected 100 tokens for key-1, got %d", summary.TotalTokens)
	}
}
