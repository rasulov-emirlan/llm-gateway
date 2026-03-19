package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/erasulov/llm-gateway/internal/cache"
	"github.com/erasulov/llm-gateway/internal/config"
	"github.com/erasulov/llm-gateway/internal/middleware"
	"github.com/erasulov/llm-gateway/internal/provider"
	"github.com/erasulov/llm-gateway/internal/telemetry"
)

type Gateway struct {
	registry *provider.Registry
	cfg      *config.Config
	metrics  *telemetry.Metrics
	cache    *cache.Cache
}

func New(registry *provider.Registry, cfg *config.Config, metrics *telemetry.Metrics, c *cache.Cache) *Gateway {
	return &Gateway{
		registry: registry,
		cfg:      cfg,
		metrics:  metrics,
		cache:    c,
	}
}

func (g *Gateway) Router() http.Handler {
	// Protected routes behind auth + rate limiting.
	api := http.NewServeMux()
	api.HandleFunc("GET /v1/models", g.handleListModels)
	api.HandleFunc("POST /v1/chat/completions", g.handleChatCompletion)

	protected := middleware.Auth(g.cfg.APIKey)(api)
	protected = middleware.RateLimit(g.cfg.RateLimit, g.cfg.RateBurst, g.metrics)(protected)

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
	models, err := g.registry.ListAllModels(r.Context())
	if err != nil {
		slog.Error("list models failed", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to list models"})
		return
	}

	// Return in OpenAI-compatible format.
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   models,
	})
}

// ChatCompletionRequest is the gateway's API request format (OpenAI-compatible).
type ChatCompletionRequest struct {
	Model       string             `json:"model"`
	Messages    []provider.Message `json:"messages"`
	Stream      bool               `json:"stream"`
	MaxTokens   int                `json:"max_tokens,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
	Stop        []string           `json:"stop,omitempty"`
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

	providerReq := provider.ChatRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.Stop,
	}

	if req.Stream {
		g.handleStreamingChat(w, r, providerReq)
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

	// Route through registry (handles fallback internally).
	resp, err := g.registry.Chat(r.Context(), providerReq)
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

func (g *Gateway) recordTokens(ctx context.Context, resp *provider.ChatResponse) {
	modelAttr := telemetry.ModelAttr(resp.Model)
	providerAttr := attribute.String("provider", resp.Provider)
	g.metrics.PromptTokens.Add(ctx, int64(resp.PromptTokens), telemetry.WithAttr(modelAttr, providerAttr))
	g.metrics.CompletionTokens.Add(ctx, int64(resp.CompletionTokens), telemetry.WithAttr(modelAttr, providerAttr))

	slog.Info("chat completion",
		"model", resp.Model,
		"provider", resp.Provider,
		"prompt_tokens", resp.PromptTokens,
		"completion_tokens", resp.CompletionTokens,
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
