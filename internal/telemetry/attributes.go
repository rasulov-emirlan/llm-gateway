package telemetry

import "go.opentelemetry.io/otel/attribute"

// Centralized OTel attribute keys used across the gateway.
// Consistent attribute naming ensures queryable traces and metrics.
const (
	AttrModel    = attribute.Key("llm.model")
	AttrProvider = attribute.Key("llm.provider")
	AttrClientIP = attribute.Key("client.ip")
	AttrCacheHit = attribute.Key("cache.hit")
	AttrStream   = attribute.Key("llm.stream")

	AttrPromptTokens     = attribute.Key("llm.usage.prompt_tokens")
	AttrCompletionTokens = attribute.Key("llm.usage.completion_tokens")
	AttrTotalTokens      = attribute.Key("llm.usage.total_tokens")
)

// ProviderAttr returns a provider attribute.
func ProviderAttr(name string) attribute.KeyValue {
	return AttrProvider.String(name)
}
