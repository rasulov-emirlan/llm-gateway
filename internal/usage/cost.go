// Package usage tracks token consumption, enforces budgets, and calculates
// costs per API key. This is the metering infrastructure that billing systems
// depend on — every AI gateway needs it.
package usage

// PricePer1M defines the price per 1 million tokens for a model.
type PricePer1M struct {
	Input  float64 // USD per 1M input tokens
	Output float64 // USD per 1M output tokens
}

// DefaultPricing returns known pricing for major models.
// Prices are approximate and should be updated from provider docs.
var DefaultPricing = map[string]PricePer1M{
	// OpenAI
	"gpt-4o":      {Input: 2.50, Output: 10.00},
	"gpt-4o-mini": {Input: 0.15, Output: 0.60},
	"gpt-4.1":     {Input: 2.00, Output: 8.00},

	// Anthropic
	"claude-opus-4-20250514":   {Input: 15.00, Output: 75.00},
	"claude-sonnet-4-20250514": {Input: 3.00, Output: 15.00},
	"claude-haiku-4-20250506":  {Input: 0.80, Output: 4.00},

	// Ollama (local, free)
	"llama3.2":  {Input: 0, Output: 0},
	"phi3":      {Input: 0, Output: 0},
	"mistral":   {Input: 0, Output: 0},
	"codellama": {Input: 0, Output: 0},
}

// CalculateCost returns the cost in USD for a given model and token counts.
func CalculateCost(model string, inputTokens, outputTokens int) float64 {
	pricing, ok := DefaultPricing[model]
	if !ok {
		return 0
	}

	inputCost := float64(inputTokens) / 1_000_000 * pricing.Input
	outputCost := float64(outputTokens) / 1_000_000 * pricing.Output
	return inputCost + outputCost
}
