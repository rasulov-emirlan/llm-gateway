// Package anthropic implements the Provider interface for the Anthropic Messages API.
// It uses raw HTTP calls to demonstrate understanding of Anthropic's unique wire format,
// which differs significantly from OpenAI's (system message handling, content blocks,
// separate input/output token counts, SSE event types).
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/erasulov/llm-gateway/internal/provider"
)

const (
	defaultBaseURL   = "https://api.anthropic.com"
	defaultVersion   = "2023-06-01"
	defaultMaxTokens = 4096
)

// Adapter implements provider.Provider for Anthropic's Messages API.
type Adapter struct {
	baseURL    string
	apiKey     string
	version    string
	httpClient *http.Client
}

// NewAdapter creates an Anthropic provider adapter.
func NewAdapter(baseURL, apiKey string) *Adapter {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Adapter{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		version: defaultVersion,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

func (a *Adapter) Name() string { return "anthropic" }

// Chat sends a non-streaming request to Anthropic's Messages API.
func (a *Adapter) Chat(ctx context.Context, req provider.ChatRequest) (*provider.ChatResponse, error) {
	start := time.Now()

	anthropicReq := toAnthropicRequest(req)
	anthropicReq.Stream = false

	body, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: create request: %w", err)
	}
	a.setHeaders(httpReq)

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic: returned %d: %s", resp.StatusCode, string(respBody))
	}

	var anthropicResp anthropicMessagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&anthropicResp); err != nil {
		return nil, fmt.Errorf("anthropic: decode response: %w", err)
	}

	// Extract text from content blocks.
	var content string
	for _, block := range anthropicResp.Content {
		if block.Type == "text" {
			content += block.Text
		}
	}

	return &provider.ChatResponse{
		ID:               anthropicResp.ID,
		Model:            anthropicResp.Model,
		Provider:         "anthropic",
		Message:          provider.Message{Role: anthropicResp.Role, Content: content},
		PromptTokens:     anthropicResp.Usage.InputTokens,
		CompletionTokens: anthropicResp.Usage.OutputTokens,
		TotalTokens:      anthropicResp.Usage.InputTokens + anthropicResp.Usage.OutputTokens,
		Latency:          time.Since(start),
	}, nil
}

// ChatStream sends a streaming request using Anthropic's SSE format.
// Anthropic streaming uses distinct event types: message_start, content_block_start,
// content_block_delta, content_block_stop, message_delta, message_stop.
func (a *Adapter) ChatStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamChunk, error) {
	anthropicReq := toAnthropicRequest(req)
	anthropicReq.Stream = true

	body, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: create request: %w", err)
	}
	a.setHeaders(httpReq)

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic: returned %d: %s", resp.StatusCode, string(respBody))
	}

	ch := make(chan provider.StreamChunk, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		a.readSSEStream(ctx, resp.Body, req.Model, ch)
	}()

	return ch, nil
}

func (a *Adapter) readSSEStream(ctx context.Context, body io.Reader, model string, ch chan<- provider.StreamChunk) {
	scanner := bufio.NewScanner(body)
	var eventType string
	var msgID string
	var inputTokens, outputTokens int

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		switch eventType {
		case "message_start":
			var event struct {
				Message struct {
					ID    string `json:"id"`
					Usage struct {
						InputTokens int `json:"input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(data), &event); err == nil {
				msgID = event.Message.ID
				inputTokens = event.Message.Usage.InputTokens
			}

		case "content_block_delta":
			var event struct {
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				select {
				case ch <- provider.StreamChunk{Err: fmt.Errorf("anthropic: unmarshal delta: %w", err)}:
				case <-ctx.Done():
				}
				return
			}

			if event.Delta.Type == "text_delta" {
				select {
				case ch <- provider.StreamChunk{
					ID:    msgID,
					Model: model,
					Delta: event.Delta.Text,
				}:
				case <-ctx.Done():
					return
				}
			}

		case "message_delta":
			var event struct {
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &event); err == nil {
				outputTokens = event.Usage.OutputTokens
			}

		case "message_stop":
			select {
			case ch <- provider.StreamChunk{
				ID:    msgID,
				Model: model,
				Done:  true,
				Usage: &provider.Usage{
					PromptTokens:     inputTokens,
					CompletionTokens: outputTokens,
					TotalTokens:      inputTokens + outputTokens,
				},
			}:
			case <-ctx.Done():
			}
			return
		}
	}
}

// ListModels returns known Anthropic models.
// Anthropic doesn't have a public /models endpoint, so we return a static list.
func (a *Adapter) ListModels(_ context.Context) ([]provider.ModelInfo, error) {
	models := []provider.ModelInfo{
		{ID: "claude-opus-4-20250514", Provider: "anthropic", OwnedBy: "anthropic", ContextWindow: 200000},
		{ID: "claude-sonnet-4-20250514", Provider: "anthropic", OwnedBy: "anthropic", ContextWindow: 200000},
		{ID: "claude-haiku-4-20250506", Provider: "anthropic", OwnedBy: "anthropic", ContextWindow: 200000},
	}
	return models, nil
}

// Health checks if the Anthropic API is reachable.
func (a *Adapter) Health(ctx context.Context) error {
	// Anthropic doesn't have a health endpoint; send a minimal request.
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/v1/messages", nil)
	if err != nil {
		return fmt.Errorf("anthropic: health check: %w", err)
	}
	a.setHeaders(httpReq)

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("anthropic: health check: %w", err)
	}
	defer resp.Body.Close()

	// Any response (even 405 Method Not Allowed) means the API is reachable.
	return nil
}

func (a *Adapter) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", a.version)
}

// --- Anthropic wire format types ---

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicMessagesRequest struct {
	Model     string             `json:"model"`
	Messages  []anthropicMessage `json:"messages"`
	System    string             `json:"system,omitempty"`
	MaxTokens int                `json:"max_tokens"`
	Stream    bool               `json:"stream,omitempty"`
}

type anthropicMessagesResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// toAnthropicRequest converts canonical request to Anthropic format.
// Key difference: Anthropic uses a separate "system" field instead of
// a system message in the messages array.
func toAnthropicRequest(req provider.ChatRequest) anthropicMessagesRequest {
	var system string
	msgs := make([]anthropicMessage, 0, len(req.Messages))

	for _, m := range req.Messages {
		if m.Role == "system" {
			system = m.Content
			continue
		}
		msgs = append(msgs, anthropicMessage{Role: m.Role, Content: m.Content})
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = defaultMaxTokens
	}

	return anthropicMessagesRequest{
		Model:     req.Model,
		Messages:  msgs,
		System:    system,
		MaxTokens: maxTokens,
	}
}
