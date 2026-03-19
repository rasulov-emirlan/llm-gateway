// Package openai implements the Provider interface for OpenAI-compatible APIs.
// It uses raw HTTP calls (no SDK) to demonstrate understanding of OpenAI's
// wire protocol. This adapter also works with any OpenAI-compatible endpoint
// (vLLM, Together, Groq, etc.) by changing the base URL.
package openai

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

// Adapter implements provider.Provider for OpenAI and compatible APIs.
type Adapter struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewAdapter creates an OpenAI provider adapter.
// baseURL should be "https://api.openai.com" for OpenAI proper.
func NewAdapter(baseURL, apiKey string) *Adapter {
	return &Adapter{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

func (a *Adapter) Name() string { return "openai" }

// Chat sends a non-streaming request to the OpenAI Chat Completions API.
func (a *Adapter) Chat(ctx context.Context, req provider.ChatRequest) (*provider.ChatResponse, error) {
	start := time.Now()

	oaiReq := toOpenAIRequest(req)
	oaiReq.Stream = false

	body, err := json.Marshal(oaiReq)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: create request: %w", err)
	}
	a.setHeaders(httpReq)

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai: returned %d: %s", resp.StatusCode, string(respBody))
	}

	var oaiResp openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
	}

	msg := provider.Message{}
	if len(oaiResp.Choices) > 0 {
		msg = provider.Message{
			Role:    oaiResp.Choices[0].Message.Role,
			Content: oaiResp.Choices[0].Message.Content,
		}
	}

	return &provider.ChatResponse{
		ID:               oaiResp.ID,
		Model:            oaiResp.Model,
		Provider:         "openai",
		Message:          msg,
		PromptTokens:     oaiResp.Usage.PromptTokens,
		CompletionTokens: oaiResp.Usage.CompletionTokens,
		TotalTokens:      oaiResp.Usage.TotalTokens,
		Latency:          time.Since(start),
	}, nil
}

// ChatStream sends a streaming request and returns a channel of chunks.
func (a *Adapter) ChatStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamChunk, error) {
	oaiReq := toOpenAIRequest(req)
	oaiReq.Stream = true
	oaiReq.StreamOptions = &streamOptions{IncludeUsage: true}

	body, err := json.Marshal(oaiReq)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: create request: %w", err)
	}
	a.setHeaders(httpReq)

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: do request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai: returned %d: %s", resp.StatusCode, string(respBody))
	}

	ch := make(chan provider.StreamChunk, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		a.readSSEStream(ctx, resp.Body, ch)
	}()

	return ch, nil
}

func (a *Adapter) readSSEStream(ctx context.Context, body io.Reader, ch chan<- provider.StreamChunk) {
	scanner := bufio.NewScanner(body)

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		if data == "[DONE]" {
			return
		}

		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			select {
			case ch <- provider.StreamChunk{Err: fmt.Errorf("openai: unmarshal chunk: %w", err)}:
			case <-ctx.Done():
			}
			return
		}

		sc := provider.StreamChunk{
			ID:    chunk.ID,
			Model: chunk.Model,
		}

		if len(chunk.Choices) > 0 {
			sc.Delta = chunk.Choices[0].Delta.Content
			sc.Done = chunk.Choices[0].FinishReason != ""
		}

		if chunk.Usage != nil {
			sc.Usage = &provider.Usage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}
		}

		select {
		case ch <- sc:
		case <-ctx.Done():
			return
		}
	}
}

// ListModels returns available models from OpenAI.
func (a *Adapter) ListModels(ctx context.Context) ([]provider.ModelInfo, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("openai: create request: %w", err)
	}
	a.setHeaders(httpReq)

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai: list models returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("openai: decode models: %w", err)
	}

	models := make([]provider.ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, provider.ModelInfo{
			ID:       m.ID,
			Provider: "openai",
			OwnedBy:  m.OwnedBy,
		})
	}

	return models, nil
}

// Health checks if the OpenAI API is reachable.
func (a *Adapter) Health(ctx context.Context) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/v1/models", nil)
	if err != nil {
		return fmt.Errorf("openai: health check: %w", err)
	}
	a.setHeaders(httpReq)

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("openai: health check: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("openai: health check returned %d", resp.StatusCode)
	}
	return nil
}

func (a *Adapter) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if a.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.apiKey)
	}
}

// --- OpenAI wire format types ---

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatRequest struct {
	Model         string          `json:"model"`
	Messages      []openAIMessage `json:"messages"`
	Stream        bool            `json:"stream"`
	MaxTokens     int             `json:"max_tokens,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	Stop          []string        `json:"stop,omitempty"`
	StreamOptions *streamOptions  `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIChatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message      openAIMessage `json:"message"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type openAIStreamChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			Role    string `json:"role"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func toOpenAIRequest(req provider.ChatRequest) openAIChatRequest {
	msgs := make([]openAIMessage, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = openAIMessage{Role: m.Role, Content: m.Content}
	}
	return openAIChatRequest{
		Model:       req.Model,
		Messages:    msgs,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.Stop,
	}
}
