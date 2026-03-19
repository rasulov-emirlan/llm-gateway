package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"go.opentelemetry.io/otel/attribute"

	"github.com/erasulov/llm-gateway/internal/provider"
	"github.com/erasulov/llm-gateway/internal/telemetry"
)

func (g *Gateway) handleStreamingChat(w http.ResponseWriter, r *http.Request, req provider.ChatRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming not supported"})
		return
	}

	// Route through registry (handles fallback internally).
	ch, usedModel, err := g.registry.ChatStream(r.Context(), req)
	if err != nil {
		slog.Error("streaming chat failed", "error", err, "model", req.Model)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "streaming chat failed"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	g.consumeStream(r.Context(), w, flusher, ch, usedModel)

	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func (g *Gateway) consumeStream(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, ch <-chan provider.StreamChunk, model string) {
	for chunk := range ch {
		if chunk.Err != nil {
			slog.Error("stream chunk error", "error", chunk.Err)
			break
		}

		// Write SSE-formatted chunk.
		data, err := json.Marshal(chunk)
		if err != nil {
			slog.Error("stream marshal error", "error", err)
			break
		}

		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()

		// Record metrics on the final chunk.
		if chunk.Done && chunk.Usage != nil {
			modelAttr := telemetry.ModelAttr(model)
			providerAttr := attribute.String("provider", "stream")
			g.metrics.PromptTokens.Add(ctx, int64(chunk.Usage.PromptTokens), telemetry.WithAttr(modelAttr, providerAttr))
			g.metrics.CompletionTokens.Add(ctx, int64(chunk.Usage.CompletionTokens), telemetry.WithAttr(modelAttr, providerAttr))

			slog.Info("streaming complete",
				"model", model,
				"prompt_tokens", chunk.Usage.PromptTokens,
				"completion_tokens", chunk.Usage.CompletionTokens,
			)
		}
	}
}
