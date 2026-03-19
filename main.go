package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/erasulov/llm-gateway/internal/apikey"
	"github.com/erasulov/llm-gateway/internal/cache"
	"github.com/erasulov/llm-gateway/internal/config"
	"github.com/erasulov/llm-gateway/internal/gateway"
	"github.com/erasulov/llm-gateway/internal/provider"
	"github.com/erasulov/llm-gateway/internal/usage"
	ollamaProvider "github.com/erasulov/llm-gateway/internal/provider/ollama"
	openaiProvider "github.com/erasulov/llm-gateway/internal/provider/openai"
	anthropicProvider "github.com/erasulov/llm-gateway/internal/provider/anthropic"
	"github.com/erasulov/llm-gateway/internal/telemetry"
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

	// Initialize cache.
	c, err := cache.New(cfg.RedisURL, cfg.CacheTTL)
	if err != nil {
		slog.Error("failed to initialize cache", "error", err)
		os.Exit(1)
	}
	defer c.Close()

	// Build provider registry.
	registry := buildRegistry(cfg)

	// Build API key store.
	keyStore := buildKeyStore(cfg)

	// Initialize usage tracker.
	tracker := usage.NewTracker()

	gw := gateway.New(registry, cfg, metrics, c, keyStore, tracker)

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      gw.Router(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		slog.Info("starting LLM gateway",
			"port", cfg.Port,
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

	// Load from file if configured.
	if cfg.APIKeysFile != "" {
		if err := store.LoadFromFile(cfg.APIKeysFile); err != nil {
			slog.Error("failed to load API keys file", "path", cfg.APIKeysFile, "error", err)
		}
	}

	// Backwards compatibility: load legacy single API_KEY as pro tier.
	store.LoadLegacyKey(cfg.APIKey)

	return store
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
