package telemetry

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

type Metrics struct {
	PromptTokens     metric.Int64Counter
	CompletionTokens metric.Int64Counter
	RequestTotal     metric.Int64Counter
	RequestDuration  metric.Float64Histogram
	CacheHits        metric.Int64Counter
	CacheMisses      metric.Int64Counter
	RateLimited      metric.Int64Counter
}

// ModelAttr returns an OTel attribute for the model name.
func ModelAttr(model string) attribute.KeyValue {
	return attribute.String("model", model)
}

// ClientAttr returns an OTel attribute for the client IP.
func ClientAttr(clientIP string) attribute.KeyValue {
	return attribute.String("client_ip", clientIP)
}

// WithAttr is a convenience wrapper for metric.WithAttributes.
func WithAttr(attrs ...attribute.KeyValue) metric.MeasurementOption {
	return metric.WithAttributes(attrs...)
}

// New initializes OTel metrics. If endpoint is empty, returns no-op metrics.
// The returned function shuts down the meter provider and should be deferred.
func New(ctx context.Context, endpoint string) (*Metrics, func(), error) {
	if endpoint == "" {
		slog.Info("otel metrics disabled (no endpoint configured)")
		return newNoopMetrics(), func() {}, nil
	}

	exporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(endpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return nil, nil, err
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(10*time.Second))),
	)
	otel.SetMeterProvider(provider)

	meter := provider.Meter("llm-gateway")
	m, err := newMetrics(meter)
	if err != nil {
		return nil, nil, err
	}

	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := provider.Shutdown(ctx); err != nil {
			slog.Error("otel shutdown error", "error", err)
		}
	}

	slog.Info("otel metrics enabled", "endpoint", endpoint)
	return m, shutdown, nil
}

func newMetrics(meter metric.Meter) (*Metrics, error) {
	promptTokens, err := meter.Int64Counter("llm_gateway_prompt_tokens_total",
		metric.WithDescription("Total prompt tokens processed"),
	)
	if err != nil {
		return nil, err
	}

	completionTokens, err := meter.Int64Counter("llm_gateway_completion_tokens_total",
		metric.WithDescription("Total completion tokens generated"),
	)
	if err != nil {
		return nil, err
	}

	requestTotal, err := meter.Int64Counter("llm_gateway_requests_total",
		metric.WithDescription("Total requests processed"),
	)
	if err != nil {
		return nil, err
	}

	requestDuration, err := meter.Float64Histogram("llm_gateway_request_duration_seconds",
		metric.WithDescription("Request duration in seconds"),
	)
	if err != nil {
		return nil, err
	}

	cacheHits, err := meter.Int64Counter("llm_gateway_cache_hits_total",
		metric.WithDescription("Total cache hits"),
	)
	if err != nil {
		return nil, err
	}

	cacheMisses, err := meter.Int64Counter("llm_gateway_cache_misses_total",
		metric.WithDescription("Total cache misses"),
	)
	if err != nil {
		return nil, err
	}

	rateLimited, err := meter.Int64Counter("llm_gateway_rate_limited_total",
		metric.WithDescription("Total requests rejected by rate limiter"),
	)
	if err != nil {
		return nil, err
	}

	return &Metrics{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		RequestTotal:     requestTotal,
		RequestDuration:  requestDuration,
		CacheHits:        cacheHits,
		CacheMisses:      cacheMisses,
		RateLimited:      rateLimited,
	}, nil
}

func newNoopMetrics() *Metrics {
	meter := noop.Meter{}
	promptTokens, _ := meter.Int64Counter("llm_gateway_prompt_tokens_total")
	completionTokens, _ := meter.Int64Counter("llm_gateway_completion_tokens_total")
	requestTotal, _ := meter.Int64Counter("llm_gateway_requests_total")
	requestDuration, _ := meter.Float64Histogram("llm_gateway_request_duration_seconds")
	cacheHits, _ := meter.Int64Counter("llm_gateway_cache_hits_total")
	cacheMisses, _ := meter.Int64Counter("llm_gateway_cache_misses_total")
	rateLimited, _ := meter.Int64Counter("llm_gateway_rate_limited_total")

	return &Metrics{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		RequestTotal:     requestTotal,
		RequestDuration:  requestDuration,
		CacheHits:        cacheHits,
		CacheMisses:      cacheMisses,
		RateLimited:      rateLimited,
	}
}
