package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

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
	ctx, span := telemetry.Tracer.Start(r.Context(), "gateway.list_models")
	defer span.End()

	models, err := g.registry.ListAllModels(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "list models failed")
		slog.Error("list models failed", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to list models"})
		return
	}

	span.SetAttributes(attribute.Int("models.count", len(models)))

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

	ctx, span := telemetry.Tracer.Start(r.Context(), "gateway.chat_completion")
	defer span.End()

	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "invalid request")
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

	span.SetAttributes(
		telemetry.AttrModel.String(req.Model),
		telemetry.AttrStream.Bool(req.Stream),
		attribute.Int("messages.count", len(req.Messages)),
	)

	modelAttr := attribute.String("model", req.Model)
	g.metrics.RequestTotal.Add(ctx, 1, telemetry.WithAttr(modelAttr))

	providerReq := provider.ChatRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.Stop,
	}

	if req.Stream {
		g.handleStreamingChat(w, r.WithContext(ctx), providerReq)
		return
	}

	// Check cache.
	cacheKey := cache.Key(req.Model, req.Messages)
	if cached, ok := g.cacheGet(ctx, cacheKey); ok {
		span.SetAttributes(telemetry.AttrCacheHit.Bool(true))
		slog.Info("cache hit", "model", req.Model)
		g.metrics.CacheHits.Add(ctx, 1, telemetry.WithAttr(modelAttr))
		g.recordDuration(ctx, start, req.Model)
		writeJSON(w, http.StatusOK, cached)
		return
	}
	span.SetAttributes(telemetry.AttrCacheHit.Bool(false))
	g.metrics.CacheMisses.Add(ctx, 1, telemetry.WithAttr(modelAttr))

	// Call provider (with tracing child span).
	resp, err := g.chatWithTracing(ctx, providerReq)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "chat completion failed")
		slog.Error("chat completion failed", "error", err, "model", req.Model)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "chat completion failed"})
		return
	}

	span.SetAttributes(
		telemetry.AttrProvider.String(resp.Provider),
		telemetry.AttrPromptTokens.Int(resp.PromptTokens),
		telemetry.AttrCompletionTokens.Int(resp.CompletionTokens),
		telemetry.AttrTotalTokens.Int(resp.TotalTokens),
	)

	g.recordTokens(ctx, resp)
	g.recordDuration(ctx, start, resp.Model)

	g.cache.Set(ctx, cacheKey, resp)

	writeJSON(w, http.StatusOK, resp)
}

// cacheGet wraps cache lookup with a tracing span.
func (g *Gateway) cacheGet(ctx context.Context, key string) (*provider.ChatResponse, bool) {
	_, span := telemetry.Tracer.Start(ctx, "cache.get")
	defer span.End()

	resp, ok := g.cache.Get(ctx, key)
	span.SetAttributes(attribute.Bool("hit", ok))
	return resp, ok
}

// chatWithTracing wraps the registry Chat call with a tracing span.
func (g *Gateway) chatWithTracing(ctx context.Context, req provider.ChatRequest) (*provider.ChatResponse, error) {
	ctx, span := telemetry.Tracer.Start(ctx, "provider.chat",
		trace.WithAttributes(telemetry.AttrModel.String(req.Model)),
	)
	defer span.End()

	resp, err := g.registry.Chat(ctx, req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	span.SetAttributes(
		telemetry.AttrProvider.String(resp.Provider),
		telemetry.AttrModel.String(resp.Model),
	)
	return resp, nil
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
