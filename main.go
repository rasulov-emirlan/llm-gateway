package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/erasulov/llm-gateway/internal/admin"
	"github.com/erasulov/llm-gateway/internal/apikey"
	"github.com/erasulov/llm-gateway/internal/cache"
	"github.com/erasulov/llm-gateway/internal/config"
	"github.com/erasulov/llm-gateway/internal/gateway"
	"github.com/erasulov/llm-gateway/internal/pipeline"
	"github.com/erasulov/llm-gateway/internal/provider"
	anthropicProvider "github.com/erasulov/llm-gateway/internal/provider/anthropic"
	ollamaProvider "github.com/erasulov/llm-gateway/internal/provider/ollama"
	openaiProvider "github.com/erasulov/llm-gateway/internal/provider/openai"
	"github.com/erasulov/llm-gateway/internal/redisclient"
	"github.com/erasulov/llm-gateway/internal/telemetry"
	"github.com/erasulov/llm-gateway/internal/usage"
)

func main() {
	cfg := config.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))
	slog.SetDefault(logger)

	ctx := context.Background()

	// Initialize telemetry (metrics + tracing).
	metrics, shutdownMetrics, err := telemetry.New(ctx, cfg.OTelEndpoint)
	if err != nil {
		slog.Error("failed to initialize metrics", "error", err)
		os.Exit(1)
	}
	defer shutdownMetrics()

	shutdownTracing, err := telemetry.InitTracing(ctx, cfg.OTelEndpoint)
	if err != nil {
		slog.Error("failed to initialize tracing", "error", err)
		os.Exit(1)
	}
	defer shutdownTracing()

	// Initialize shared Redis client (nil if REDIS_URL is empty).
	rdb, err := redisclient.New(cfg.RedisURL)
	if err != nil {
		slog.Error("failed to connect to redis", "error", err)
		os.Exit(1)
	}
	if rdb != nil {
		defer rdb.Close()
	}

	// Initialize cache.
	c := cache.New(rdb, cfg.CacheTTL)

	// Build provider registry.
	registry := buildRegistry(cfg)

	// Build API key store.
	keyStore := buildKeyStore(cfg)

	// Initialize usage tracker — Redis-backed if available, in-memory otherwise.
	var tracker usage.Tracker
	if rdb != nil {
		tracker = usage.NewRedisTracker(rdb)
		slog.Info("usage tracker using redis (distributed mode)")
	} else {
		tracker = usage.NewMemoryTracker()
		slog.Info("usage tracker using in-memory (single-instance mode)")
	}

	// Build content processing pipeline.
	pipe := buildPipeline()

	gw := gateway.New(registry, cfg, metrics, c, keyStore, tracker, rdb, pipe)

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      gw.Router(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start admin server on separate port.
	adminSrv := admin.NewServer(registry, keyStore, tracker, gw.Admission())
	go func() {
		if err := adminSrv.ListenAndServe(":" + cfg.AdminPort); err != nil && err != http.ErrServerClosed {
			slog.Error("admin server error", "error", err)
		}
	}()

	go func() {
		slog.Info("starting LLM gateway",
			"port", cfg.Port,
			"admin_port", cfg.AdminPort,
			"providers", providerNames(cfg.Providers),
			"fallback_models", cfg.FallbackModels,
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down server")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server forced shutdown", "error", err)
	}
}

// buildRegistry creates a provider registry from config and registers all
// enabled providers with their model routes.
func buildRegistry(cfg *config.Config) *provider.Registry {
	registry := provider.NewRegistry(cfg.FallbackModels)

	for _, pc := range cfg.Providers {
		if !pc.Enabled {
			slog.Info("provider disabled, skipping", "name", pc.Name)
			continue
		}

		var p provider.Provider
		switch pc.Name {
		case "ollama":
			p = ollamaProvider.NewAdapter(pc.BaseURL)
		case "openai":
			p = openaiProvider.NewAdapter(pc.BaseURL, pc.APIKey)
		case "anthropic":
			p = anthropicProvider.NewAdapter(pc.BaseURL, pc.APIKey)
		default:
			slog.Warn("unknown provider type, skipping", "name", pc.Name)
			continue
		}

		registry.Register(p, pc.Models)
	}

	return registry
}

// buildKeyStore creates and populates the API key store.
func buildKeyStore(cfg *config.Config) *apikey.Store {
	store := apikey.NewStore()

	if cfg.APIKeysFile != "" {
		if err := store.LoadFromFile(cfg.APIKeysFile); err != nil {
			slog.Error("failed to load API keys file", "path", cfg.APIKeysFile, "error", err)
		}
	}

	store.LoadLegacyKey(cfg.APIKey)
	return store
}

// buildPipeline creates the content processing pipeline with default processors.
func buildPipeline() *pipeline.Pipeline {
	pipe := pipeline.New()
	pipe.Register(pipeline.NewPIIDetector())
	pipe.Register(pipeline.NewPromptInjectionDetector())
	return pipe
}

func providerNames(providers []config.ProviderConfig) []string {
	names := make([]string, 0, len(providers))
	for _, p := range providers {
		if p.Enabled {
			names = append(names, p.Name)
		}
	}
	return names
}
