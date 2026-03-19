package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/erasulov/llm-gateway/internal/cache"
	"github.com/erasulov/llm-gateway/internal/config"
	"github.com/erasulov/llm-gateway/internal/gateway"
	"github.com/erasulov/llm-gateway/internal/ollama"
	"github.com/erasulov/llm-gateway/internal/telemetry"
)

func main() {
	cfg := config.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))
	slog.SetDefault(logger)

	ctx := context.Background()

	// Initialize telemetry.
	metrics, shutdownMetrics, err := telemetry.New(ctx, cfg.OTelEndpoint)
	if err != nil {
		slog.Error("failed to initialize telemetry", "error", err)
		os.Exit(1)
	}
	defer shutdownMetrics()

	// Initialize cache.
	c, err := cache.New(cfg.RedisURL, cfg.CacheTTL)
	if err != nil {
		slog.Error("failed to initialize cache", "error", err)
		os.Exit(1)
	}
	defer c.Close()

	ollamaClient := ollama.NewClient(cfg.OllamaURL)
	gw := gateway.New(ollamaClient, cfg, metrics, c)

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
			"ollama_url", cfg.OllamaURL,
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
