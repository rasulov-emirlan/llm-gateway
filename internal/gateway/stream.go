package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/erasulov/llm-gateway/internal/ollama"
	"github.com/erasulov/llm-gateway/internal/telemetry"
)

func (g *Gateway) handleStreamingChat(w http.ResponseWriter, r *http.Request, req ollama.ChatRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming not supported"})
		return
	}

	// Try fallback models until one establishes a stream.
	body, usedModel, err := g.streamWithFallback(r.Context(), req)
	if err != nil {
		slog.Error("streaming chat failed", "error", err, "model", req.Model)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "streaming chat failed"})
		return
	}
	defer body.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		fmt.Fprintf(w, "data: %s\n\n", line)
		flusher.Flush()

		var chunk ollama.ChatResponse
		if err := json.Unmarshal(line, &chunk); err == nil && chunk.Done {
			modelAttr := telemetry.ModelAttr(usedModel)
			g.metrics.PromptTokens.Add(r.Context(), int64(chunk.PromptEvalCount), telemetry.WithAttr(modelAttr))
			g.metrics.CompletionTokens.Add(r.Context(), int64(chunk.EvalCount), telemetry.WithAttr(modelAttr))

			slog.Info("streaming complete",
				"model", usedModel,
				"prompt_tokens", chunk.PromptEvalCount,
				"completion_tokens", chunk.EvalCount,
			)
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Error("stream read error", "error", err)
	}

	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// streamWithFallback tries each model in the fallback chain until one establishes a stream.
func (g *Gateway) streamWithFallback(ctx context.Context, req ollama.ChatRequest) (io.ReadCloser, string, error) {
	models := g.buildModelChain(req.Model)
	var lastErr error

	for _, model := range models {
		req.Model = model
		body, err := g.ollama.ChatStream(ctx, req)
		if err != nil {
			slog.Warn("stream model failed, trying fallback", "model", model, "error", err)
			lastErr = err
			continue
		}
		if model != models[0] {
			slog.Info("stream fallback succeeded", "original", models[0], "fallback", model)
		}
		return body, model, nil
	}

	return nil, "", fmt.Errorf("all models failed for streaming, last error: %w", lastErr)
}
