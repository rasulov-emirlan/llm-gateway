// Package provider defines the canonical types and interfaces for LLM provider
// abstraction. All providers (Ollama, OpenAI, Anthropic, etc.) implement the
// Provider interface using the gateway's unified message format.
//
// This is the core abstraction of the gateway — it decouples the API layer
// from any specific LLM backend, enabling multi-provider routing, fallback
// chains, and load balancing.
package provider

import (
	"context"
	"time"
)

// Provider is the interface that all LLM backends must implement.
// Each provider translates between the gateway's canonical types and
// the provider's native wire format.
type Provider interface {
	// Name returns the provider's identifier (e.g., "ollama", "openai", "anthropic").
	Name() string

	// Chat sends a non-streaming chat completion request and returns the full response.
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)

	// ChatStream sends a streaming chat request and returns a channel of chunks.
	// The channel is closed when the stream is complete or an error occurs.
	// The caller should check StreamChunk.Err for per-chunk errors.
	ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error)

	// ListModels returns all models available from this provider.
	ListModels(ctx context.Context) ([]ModelInfo, error)

	// Health checks whether the provider is reachable and operational.
	Health(ctx context.Context) error
}

// Message is the gateway's canonical message format (OpenAI-compatible).
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the gateway's canonical chat completion request.
type ChatRequest struct {
	Model       string   `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool     `json:"stream,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	Stop        []string `json:"stop,omitempty"`
}

// ChatResponse is the gateway's canonical chat completion response.
type ChatResponse struct {
	ID               string  `json:"id"`
	Model            string  `json:"model"`
	Provider         string  `json:"provider"`
	Message          Message `json:"message"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	Latency          time.Duration `json:"-"`
}

// StreamChunk represents a single piece of a streaming response.
type StreamChunk struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Delta string `json:"delta"`
	Done  bool   `json:"done"`

	// Usage is populated only on the final chunk (when Done is true).
	Usage *Usage `json:"usage,omitempty"`

	// Err is set if an error occurred while reading this chunk.
	Err error `json:"-"`
}

// Usage contains token usage information.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ModelInfo describes a model available through a provider.
type ModelInfo struct {
	ID           string `json:"id"`
	Provider     string `json:"provider"`
	OwnedBy      string `json:"owned_by,omitempty"`
	ContextWindow int   `json:"context_window,omitempty"`
}
