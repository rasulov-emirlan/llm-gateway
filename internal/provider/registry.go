package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/erasulov/llm-gateway/internal/resilience"
)

// Registry manages provider instances and routes requests to the appropriate
// provider based on model name. Each provider is wrapped with a circuit breaker
// for resilience, and calls are retried with exponential backoff.
type Registry struct {
	mu sync.RWMutex

	// providers maps provider name → Provider instance.
	providers map[string]Provider

	// breakers maps provider name → CircuitBreaker.
	breakers map[string]*resilience.CircuitBreaker

	// modelRoutes maps model name → provider name.
	modelRoutes map[string]string

	// fallbackModels is the ordered fallback chain for model failures.
	fallbackModels []string

	// retryCfg is the retry configuration for provider calls.
	retryCfg resilience.RetryConfig

	// cbCfg is the circuit breaker configuration for providers.
	cbCfg resilience.CircuitBreakerConfig
}

// NewRegistry creates an empty provider registry with default resilience configs.
func NewRegistry(fallbackModels []string) *Registry {
	return &Registry{
		providers:      make(map[string]Provider),
		breakers:       make(map[string]*resilience.CircuitBreaker),
		modelRoutes:    make(map[string]string),
		fallbackModels: fallbackModels,
		retryCfg:       resilience.DefaultRetryConfig(),
		cbCfg:          resilience.DefaultCircuitBreakerConfig(),
	}
}

// SetRetryConfig overrides the default retry configuration.
func (r *Registry) SetRetryConfig(cfg resilience.RetryConfig) {
	r.retryCfg = cfg
}

// SetCircuitBreakerConfig overrides the default circuit breaker configuration.
func (r *Registry) SetCircuitBreakerConfig(cfg resilience.CircuitBreakerConfig) {
	r.cbCfg = cfg
}

// Register adds a provider, creates its circuit breaker, and maps models.
func (r *Registry) Register(p Provider, models []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.providers[p.Name()] = p
	r.breakers[p.Name()] = resilience.NewCircuitBreaker(p.Name(), r.cbCfg)

	for _, model := range models {
		r.modelRoutes[model] = p.Name()
		slog.Debug("registered model route", "model", model, "provider", p.Name())
	}
	slog.Info("registered provider", "name", p.Name(), "models", models)
}

// ResolveProvider returns the provider responsible for a given model.
func (r *Registry) ResolveProvider(model string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providerName, ok := r.modelRoutes[model]
	if !ok {
		return nil, fmt.Errorf("no provider registered for model %q", model)
	}

	p, ok := r.providers[providerName]
	if !ok {
		return nil, fmt.Errorf("provider %q not found (model %q)", providerName, model)
	}

	return p, nil
}

// getBreaker returns the circuit breaker for a provider.
func (r *Registry) getBreaker(providerName string) *resilience.CircuitBreaker {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.breakers[providerName]
}

// Chat routes a request through circuit breaker + retry, with fallback.
func (r *Registry) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	chain := r.buildModelChain(req.Model)
	var lastErr error

	for _, model := range chain {
		p, err := r.ResolveProvider(model)
		if err != nil {
			slog.Warn("no provider for model, skipping", "model", model, "error", err)
			lastErr = err
			continue
		}

		cb := r.getBreaker(p.Name())

		// Skip providers with open circuit breakers.
		if cb.State() == resilience.StateOpen {
			slog.Warn("circuit breaker open, skipping provider",
				"provider", p.Name(), "model", model)
			lastErr = fmt.Errorf("%s: %w", p.Name(), resilience.ErrCircuitOpen)
			continue
		}

		var resp *ChatResponse
		req.Model = model

		// Retry with circuit breaker wrapping.
		err = resilience.Retry(ctx, r.retryCfg, func(ctx context.Context) error {
			return cb.Execute(ctx, func(ctx context.Context) error {
				var chatErr error
				resp, chatErr = p.Chat(ctx, req)
				return chatErr
			})
		})

		if err != nil {
			// Don't retry if circuit is now open.
			if errors.Is(err, resilience.ErrCircuitOpen) {
				slog.Warn("circuit opened during retry, trying fallback",
					"model", model, "provider", p.Name())
			} else {
				slog.Warn("provider call failed after retries, trying fallback",
					"model", model, "provider", p.Name(), "error", err)
			}
			lastErr = err
			continue
		}

		if model != chain[0] {
			slog.Info("fallback succeeded", "original", chain[0], "fallback", model, "provider", p.Name())
		}
		return resp, nil
	}

	return nil, fmt.Errorf("all models failed, last error: %w", lastErr)
}

// ChatStream routes a streaming request with circuit breaker + fallback.
// Note: streaming requests are not retried (you can't replay a partial stream).
func (r *Registry) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, string, error) {
	chain := r.buildModelChain(req.Model)
	var lastErr error

	for _, model := range chain {
		p, err := r.ResolveProvider(model)
		if err != nil {
			slog.Warn("no provider for model, skipping", "model", model, "error", err)
			lastErr = err
			continue
		}

		cb := r.getBreaker(p.Name())

		if cb.State() == resilience.StateOpen {
			slog.Warn("circuit breaker open, skipping provider",
				"provider", p.Name(), "model", model)
			lastErr = fmt.Errorf("%s: %w", p.Name(), resilience.ErrCircuitOpen)
			continue
		}

		req.Model = model
		var ch <-chan StreamChunk

		// Circuit breaker wraps the stream establishment (not the reading).
		err = cb.Execute(ctx, func(ctx context.Context) error {
			var streamErr error
			ch, streamErr = p.ChatStream(ctx, req)
			return streamErr
		})

		if err != nil {
			slog.Warn("stream failed, trying fallback",
				"model", model, "provider", p.Name(), "error", err)
			lastErr = err
			continue
		}

		if model != chain[0] {
			slog.Info("stream fallback succeeded", "original", chain[0], "fallback", model, "provider", p.Name())
		}
		return ch, model, nil
	}

	return nil, "", fmt.Errorf("all models failed for streaming, last error: %w", lastErr)
}

// ListAllModels aggregates models from all registered providers.
func (r *Registry) ListAllModels(ctx context.Context) ([]ModelInfo, error) {
	r.mu.RLock()
	providers := make([]Provider, 0, len(r.providers))
	for _, p := range r.providers {
		providers = append(providers, p)
	}
	r.mu.RUnlock()

	var allModels []ModelInfo
	for _, p := range providers {
		models, err := p.ListModels(ctx)
		if err != nil {
			slog.Warn("failed to list models from provider", "provider", p.Name(), "error", err)
			continue
		}
		allModels = append(allModels, models...)
	}

	return allModels, nil
}

// buildModelChain returns [primaryModel, fallback1, fallback2, ...] with duplicates removed.
func (r *Registry) buildModelChain(primary string) []string {
	seen := map[string]bool{primary: true}
	chain := []string{primary}

	for _, m := range r.fallbackModels {
		if !seen[m] {
			seen[m] = true
			chain = append(chain, m)
		}
	}

	return chain
}
