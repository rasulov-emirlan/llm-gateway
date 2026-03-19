package provider

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// Registry manages provider instances and routes requests to the appropriate
// provider based on model name. It maintains a model→provider mapping and
// supports health-aware routing.
//
// This is analogous to how LiteLLM and Portkey route requests across providers:
// the caller specifies a model name, and the registry resolves which backend
// to hit.
type Registry struct {
	mu sync.RWMutex

	// providers maps provider name → Provider instance.
	providers map[string]Provider

	// modelRoutes maps model name → provider name.
	// Populated from config: each provider declares which models it serves.
	modelRoutes map[string]string

	// fallbackModels is the ordered fallback chain for model failures.
	fallbackModels []string
}

// NewRegistry creates an empty provider registry.
func NewRegistry(fallbackModels []string) *Registry {
	return &Registry{
		providers:      make(map[string]Provider),
		modelRoutes:    make(map[string]string),
		fallbackModels: fallbackModels,
	}
}

// Register adds a provider and maps its models to it.
func (r *Registry) Register(p Provider, models []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.providers[p.Name()] = p
	for _, model := range models {
		r.modelRoutes[model] = p.Name()
		slog.Debug("registered model route", "model", model, "provider", p.Name())
	}
	slog.Info("registered provider", "name", p.Name(), "models", models)
}

// ResolveProvider returns the provider responsible for a given model.
// Returns an error if no provider is registered for the model.
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

// Chat routes a chat request to the appropriate provider, with fallback support.
// It tries the requested model first, then iterates through the fallback chain.
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

		req.Model = model
		resp, err := p.Chat(ctx, req)
		if err != nil {
			slog.Warn("provider call failed, trying fallback",
				"model", model,
				"provider", p.Name(),
				"error", err,
			)
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

// ChatStream routes a streaming request to the appropriate provider, with fallback.
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

		req.Model = model
		ch, err := p.ChatStream(ctx, req)
		if err != nil {
			slog.Warn("stream failed, trying fallback",
				"model", model,
				"provider", p.Name(),
				"error", err,
			)
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
