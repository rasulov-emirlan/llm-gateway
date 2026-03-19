// Package ollama implements the Provider interface for Ollama backends.
// It wraps the existing Ollama HTTP client and translates between the
// gateway's canonical types and Ollama's native wire format.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/erasulov/llm-gateway/internal/provider"
)

// Adapter implements provider.Provider for Ollama.
type Adapter struct {
	baseURL    string
	httpClient *http.Client
}

// NewAdapter creates a new Ollama provider adapter.
func NewAdapter(baseURL string) *Adapter {
	return &Adapter{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

func (a *Adapter) Name() string { return "ollama" }

// Chat sends a non-streaming request to Ollama and translates the response.
func (a *Adapter) Chat(ctx context.Context, req provider.ChatRequest) (*provider.ChatResponse, error) {
	start := time.Now()

	ollamaReq := toOllamaRequest(req)
	ollamaReq.Stream = false

	body, err := json.Marshal(ollamaReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama: returned %d: %s", resp.StatusCode, string(respBody))
	}

	var ollamaResp ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}

	return &provider.ChatResponse{
		ID:               fmt.Sprintf("ollama-%d", time.Now().UnixNano()),
		Model:            ollamaResp.Model,
		Provider:         "ollama",
		Message:          provider.Message{Role: ollamaResp.Message.Role, Content: ollamaResp.Message.Content},
		PromptTokens:     ollamaResp.PromptEvalCount,
		CompletionTokens: ollamaResp.EvalCount,
		TotalTokens:      ollamaResp.PromptEvalCount + ollamaResp.EvalCount,
		Latency:          time.Since(start),
	}, nil
}

// ChatStream sends a streaming request and returns a channel of canonical chunks.
func (a *Adapter) ChatStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamChunk, error) {
	ollamaReq := toOllamaRequest(req)
	ollamaReq.Stream = true

	body, err := json.Marshal(ollamaReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama: returned %d: %s", resp.StatusCode, string(respBody))
	}

	ch := make(chan provider.StreamChunk, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		a.readStream(ctx, resp.Body, req.Model, ch)
	}()

	return ch, nil
}

func (a *Adapter) readStream(ctx context.Context, body io.Reader, model string, ch chan<- provider.StreamChunk) {
	scanner := bufio.NewScanner(body)
	id := fmt.Sprintf("ollama-%d", time.Now().UnixNano())

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var chunk ollamaChatResponse
		if err := json.Unmarshal(line, &chunk); err != nil {
			select {
			case ch <- provider.StreamChunk{Err: fmt.Errorf("ollama: unmarshal chunk: %w", err)}:
			case <-ctx.Done():
			}
			return
		}

		sc := provider.StreamChunk{
			ID:    id,
			Model: chunk.Model,
			Delta: chunk.Message.Content,
			Done:  chunk.Done,
		}

		if chunk.Done {
			sc.Usage = &provider.Usage{
				PromptTokens:     chunk.PromptEvalCount,
				CompletionTokens: chunk.EvalCount,
				TotalTokens:      chunk.PromptEvalCount + chunk.EvalCount,
			}
		}

		select {
		case ch <- sc:
		case <-ctx.Done():
			return
		}
	}

	if err := scanner.Err(); err != nil {
		select {
		case ch <- provider.StreamChunk{Err: fmt.Errorf("ollama: stream read: %w", err)}:
		case <-ctx.Done():
		}
	}
}

// ListModels returns available models from Ollama.
func (a *Adapter) ListModels(ctx context.Context) ([]provider.ModelInfo, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("ollama: create request: %w", err)
	}

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: do request: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Models []struct {
			Name       string `json:"name"`
			ModifiedAt string `json:"modified_at"`
			Size       int64  `json:"size"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}

	models := make([]provider.ModelInfo, 0, len(result.Models))
	for _, m := range result.Models {
		models = append(models, provider.ModelInfo{
			ID:       m.Name,
			Provider: "ollama",
			OwnedBy:  "local",
		})
	}

	return models, nil
}

// Health checks if Ollama is reachable by listing models.
func (a *Adapter) Health(ctx context.Context) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/api/tags", nil)
	if err != nil {
		return fmt.Errorf("ollama: health check: %w", err)
	}

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("ollama: health check: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama: health check returned %d", resp.StatusCode)
	}
	return nil
}

// --- Ollama wire format types ---

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
}

type ollamaChatResponse struct {
	Model           string        `json:"model"`
	Message         ollamaMessage `json:"message"`
	Done            bool          `json:"done"`
	TotalDuration   int64         `json:"total_duration"`
	PromptEvalCount int           `json:"prompt_eval_count"`
	EvalCount       int           `json:"eval_count"`
}

func toOllamaRequest(req provider.ChatRequest) ollamaChatRequest {
	msgs := make([]ollamaMessage, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = ollamaMessage{Role: m.Role, Content: m.Content}
	}
	return ollamaChatRequest{
		Model:    req.Model,
		Messages: msgs,
	}
}
