package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/erasulov/llm-gateway/internal/cache"
	"github.com/erasulov/llm-gateway/internal/config"
	"github.com/erasulov/llm-gateway/internal/middleware"
	"github.com/erasulov/llm-gateway/internal/ollama"
	"github.com/erasulov/llm-gateway/internal/telemetry"
)

type Gateway struct {
	ollama  *ollama.Client
	cfg     *config.Config
	metrics *telemetry.Metrics
	cache   *cache.Cache
}

func New(ollamaClient *ollama.Client, cfg *config.Config, metrics *telemetry.Metrics, c *cache.Cache) *Gateway {
	return &Gateway{
		ollama:  ollamaClient,
		cfg:     cfg,
		metrics: metrics,
		cache:   c,
	}
}

func (g *Gateway) Router() http.Handler {
	// Protected routes behind auth + rate limiting.
	api := http.NewServeMux()
	api.HandleFunc("GET /v1/models", g.handleListModels)
	api.HandleFunc("POST /v1/chat/completions", g.handleChatCompletion)

	protected := middleware.Auth(g.cfg.APIKey)(api)
	protected = middleware.RateLimit(g.cfg.RateLimit, g.cfg.RateBurst)(protected)

	// Top-level mux: health is public, everything else is protected.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", g.handleHealth)
	mux.Handle("/", protected)

	return middleware.Logging(mux)
}

func (g *Gateway) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (g *Gateway) handleListModels(w http.ResponseWriter, r *http.Request) {
	models, err := g.ollama.ListModels(r.Context())
	if err != nil {
		slog.Error("list models failed", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to list models"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// ChatCompletionRequest is the gateway's API request format.
type ChatCompletionRequest struct {
	Model    string           `json:"model"`
	Messages []ollama.Message `json:"messages"`
	Stream   bool             `json:"stream"`
}

func (g *Gateway) handleChatCompletion(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if req.Model == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model is required"})
		return
	}

	if len(req.Messages) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "messages are required"})
		return
	}

	modelAttr := attribute.String("model", req.Model)
	g.metrics.RequestTotal.Add(r.Context(), 1, telemetry.WithAttr(modelAttr))

	ollamaReq := ollama.ChatRequest{
		Model:    req.Model,
		Messages: req.Messages,
	}

	if req.Stream {
		g.handleStreamingChat(w, r, ollamaReq)
		return
	}

	// Check cache for non-streaming requests.
	cacheKey := cache.Key(req.Model, req.Messages)
	if cached, ok := g.cache.Get(r.Context(), cacheKey); ok {
		slog.Info("cache hit", "model", req.Model)
		g.metrics.CacheHits.Add(r.Context(), 1, telemetry.WithAttr(modelAttr))
		g.recordDuration(r.Context(), start, req.Model)
		writeJSON(w, http.StatusOK, cached)
		return
	}
	g.metrics.CacheMisses.Add(r.Context(), 1, telemetry.WithAttr(modelAttr))

	resp, err := g.chatWithFallback(r.Context(), ollamaReq)
	if err != nil {
		slog.Error("chat completion failed", "error", err, "model", req.Model)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "chat completion failed"})
		return
	}

	g.recordTokens(r.Context(), resp)
	g.recordDuration(r.Context(), start, resp.Model)

	// Cache the response.
	g.cache.Set(r.Context(), cacheKey, resp)

	writeJSON(w, http.StatusOK, resp)
}

// chatWithFallback tries the primary model, then fallback models in order.
func (g *Gateway) chatWithFallback(ctx context.Context, req ollama.ChatRequest) (*ollama.ChatResponse, error) {
	models := g.buildModelChain(req.Model)
	var lastErr error

	for _, model := range models {
		req.Model = model
		resp, err := g.ollama.Chat(ctx, req)
		if err != nil {
			slog.Warn("model failed, trying fallback", "model", model, "error", err)
			lastErr = err
			continue
		}
		if model != models[0] {
			slog.Info("fallback succeeded", "original", models[0], "fallback", model)
		}
		return resp, nil
	}

	return nil, fmt.Errorf("all models failed, last error: %w", lastErr)
}

// buildModelChain returns [primaryModel, fallback1, fallback2, ...] with duplicates removed.
func (g *Gateway) buildModelChain(primary string) []string {
	seen := map[string]bool{primary: true}
	chain := []string{primary}

	for _, m := range g.cfg.FallbackModels {
		if !seen[m] {
			seen[m] = true
			chain = append(chain, m)
		}
	}

	return chain
}

func (g *Gateway) recordTokens(ctx context.Context, resp *ollama.ChatResponse) {
	modelAttr := telemetry.ModelAttr(resp.Model)
	g.metrics.PromptTokens.Add(ctx, int64(resp.PromptEvalCount), telemetry.WithAttr(modelAttr))
	g.metrics.CompletionTokens.Add(ctx, int64(resp.EvalCount), telemetry.WithAttr(modelAttr))

	slog.Info("chat completion",
		"model", resp.Model,
		"prompt_tokens", resp.PromptEvalCount,
		"completion_tokens", resp.EvalCount,
	)
}

func (g *Gateway) recordDuration(ctx context.Context, start time.Time, model string) {
	duration := time.Since(start).Seconds()
	g.metrics.RequestDuration.Record(ctx, duration, telemetry.WithAttr(telemetry.ModelAttr(model)))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
