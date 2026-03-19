package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/erasulov/llm-gateway/internal/provider"
	"github.com/erasulov/llm-gateway/internal/telemetry"
)

func (g *Gateway) handleStreamingChat(w http.ResponseWriter, r *http.Request, req provider.ChatRequest) {
	ctx := r.Context()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming not supported"})
		return
	}

	// Start a child span for the streaming provider call.
	ctx, span := telemetry.Tracer.Start(ctx, "provider.chat_stream",
		trace.WithAttributes(telemetry.AttrModel.String(req.Model)),
	)
	defer span.End()

	ch, usedModel, err := g.registry.ChatStream(ctx, req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "streaming chat failed")
		slog.Error("streaming chat failed", "error", err, "model", req.Model)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "streaming chat failed"})
		return
	}

	span.SetAttributes(telemetry.AttrModel.String(usedModel))
	if usedModel != req.Model {
		span.AddEvent("fallback_used", trace.WithAttributes(
			attribute.String("original_model", req.Model),
			attribute.String("fallback_model", usedModel),
		))
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	g.consumeStream(ctx, w, flusher, ch, usedModel, span)

	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func (g *Gateway) consumeStream(
	ctx context.Context,
	w http.ResponseWriter,
	flusher http.Flusher,
	ch <-chan provider.StreamChunk,
	model string,
	span trace.Span,
) {
	chunkCount := 0

	for chunk := range ch {
		if chunk.Err != nil {
			span.RecordError(chunk.Err)
			slog.Error("stream chunk error", "error", chunk.Err)
			break
		}

		chunkCount++

		data, err := json.Marshal(chunk)
		if err != nil {
			slog.Error("stream marshal error", "error", err)
			break
		}

		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()

		if chunk.Done && chunk.Usage != nil {
			span.SetAttributes(
				telemetry.AttrPromptTokens.Int(chunk.Usage.PromptTokens),
				telemetry.AttrCompletionTokens.Int(chunk.Usage.CompletionTokens),
				telemetry.AttrTotalTokens.Int(chunk.Usage.TotalTokens),
				attribute.Int("stream.chunk_count", chunkCount),
			)

			modelAttr := telemetry.ModelAttr(model)
			g.metrics.PromptTokens.Add(ctx, int64(chunk.Usage.PromptTokens), telemetry.WithAttr(modelAttr))
			g.metrics.CompletionTokens.Add(ctx, int64(chunk.Usage.CompletionTokens), telemetry.WithAttr(modelAttr))

			slog.Info("streaming complete",
				"model", model,
				"prompt_tokens", chunk.Usage.PromptTokens,
				"completion_tokens", chunk.Usage.CompletionTokens,
				"chunks", chunkCount,
			)
		}
	}
}
