package telemetry

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Tracer is the package-level tracer used across the gateway.
var Tracer trace.Tracer

func init() {
	// Default to a noop tracer until InitTracing is called.
	Tracer = trace.NewNoopTracerProvider().Tracer("llm-gateway")
}

// InitTracing sets up the OpenTelemetry trace pipeline with OTLP gRPC export.
// If endpoint is empty, tracing is disabled (noop). The returned function
// shuts down the trace provider and should be deferred.
func InitTracing(ctx context.Context, endpoint string) (func(), error) {
	if endpoint == "" {
		slog.Info("otel tracing disabled (no endpoint configured)")
		return func() {}, nil
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(5*time.Second),
		),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	Tracer = provider.Tracer("llm-gateway")

	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := provider.Shutdown(ctx); err != nil {
			slog.Error("otel trace shutdown error", "error", err)
		}
	}

	slog.Info("otel tracing enabled", "endpoint", endpoint)
	return shutdown, nil
}

// SpanFromContext extracts trace_id and span_id from the current span for
// log correlation. Returns empty strings if no active span.
func SpanFromContext(ctx context.Context) (traceID, spanID string) {
	span := trace.SpanFromContext(ctx)
	sc := span.SpanContext()
	if sc.HasTraceID() {
		traceID = sc.TraceID().String()
	}
	if sc.HasSpanID() {
		spanID = sc.SpanID().String()
	}
	return
}
