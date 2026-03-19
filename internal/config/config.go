package config

import (
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// ProviderConfig defines a single LLM provider backend.
type ProviderConfig struct {
	Name    string   `json:"name"`     // "ollama", "openai", "anthropic"
	BaseURL string   `json:"base_url"` // e.g., "http://localhost:11434", "https://api.openai.com"
	APIKey  string   `json:"api_key"`
	Models  []string `json:"models"`   // models this provider serves (used for routing)
	Enabled bool     `json:"enabled"`
}

type Config struct {
	Port           string
	APIKey         string
	LogLevel       slog.Level
	RateLimit      float64
	RateBurst      int
	FallbackModels []string
	RedisURL       string
	CacheTTL       time.Duration
	OTelEndpoint   string

	// Multi-provider configuration.
	Providers []ProviderConfig

	// API key management.
	APIKeysFile string // path to JSON file with API key definitions

	// Admission control.
	MaxConcurrent int // max concurrent provider calls (0 = 50)
	MaxQueueDepth int // max queued requests (0 = 100)

	// Admin API.
	AdminPort string
}

func Load() *Config {
	cfg := &Config{
		Port:           getEnv("PORT", "8080"),
		APIKey:         getEnv("API_KEY", ""),
		LogLevel:       parseLogLevel(getEnv("LOG_LEVEL", "info")),
		RateLimit:      getEnvFloat("RATE_LIMIT", 10),
		RateBurst:      getEnvInt("RATE_BURST", 20),
		FallbackModels: getEnvList("FALLBACK_MODELS"),
		RedisURL:       getEnv("REDIS_URL", ""),
		CacheTTL:       getEnvDuration("CACHE_TTL", 5*time.Minute),
		OTelEndpoint:   getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		APIKeysFile:    getEnv("API_KEYS_FILE", ""),
		MaxConcurrent:  getEnvInt("MAX_CONCURRENT", 50),
		MaxQueueDepth:  getEnvInt("MAX_QUEUE_DEPTH", 100),
		AdminPort:      getEnv("ADMIN_PORT", "9091"),
	}

	cfg.Providers = loadProviders()
	return cfg
}

// loadProviders builds the provider list. If PROVIDERS_CONFIG is set (JSON file path),
// it loads from that file. Otherwise, it builds a default Ollama provider from
// legacy env vars for backwards compatibility.
func loadProviders() []ProviderConfig {
	configPath := os.Getenv("PROVIDERS_CONFIG")
	if configPath != "" {
		providers, err := loadProvidersFromFile(configPath)
		if err != nil {
			slog.Error("failed to load providers config, falling back to env", "path", configPath, "error", err)
		} else {
			return providers
		}
	}

	// Default: build from legacy env vars for backwards compatibility.
	var providers []ProviderConfig

	// Ollama (always present if OLLAMA_URL is set or default).
	ollamaURL := getEnv("OLLAMA_URL", "http://localhost:11434")
	ollamaModels := getEnvList("OLLAMA_MODELS")
	providers = append(providers, ProviderConfig{
		Name:    "ollama",
		BaseURL: ollamaURL,
		Models:  ollamaModels,
		Enabled: true,
	})

	// OpenAI (optional, enabled if OPENAI_API_KEY is set).
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		openaiModels := getEnvList("OPENAI_MODELS")
		if len(openaiModels) == 0 {
			openaiModels = []string{"gpt-4o", "gpt-4o-mini", "gpt-4.1"}
		}
		providers = append(providers, ProviderConfig{
			Name:    "openai",
			BaseURL: getEnv("OPENAI_BASE_URL", "https://api.openai.com"),
			APIKey:  key,
			Models:  openaiModels,
			Enabled: true,
		})
	}

	// Anthropic (optional, enabled if ANTHROPIC_API_KEY is set).
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		anthropicModels := getEnvList("ANTHROPIC_MODELS")
		if len(anthropicModels) == 0 {
			anthropicModels = []string{"claude-sonnet-4-20250514", "claude-haiku-4-20250506"}
		}
		providers = append(providers, ProviderConfig{
			Name:    "anthropic",
			BaseURL: getEnv("ANTHROPIC_BASE_URL", "https://api.anthropic.com"),
			APIKey:  key,
			Models:  anthropicModels,
			Enabled: true,
		})
	}

	return providers
}

func loadProvidersFromFile(path string) ([]ProviderConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var providers []ProviderConfig
	if err := json.Unmarshal(data, &providers); err != nil {
		return nil, err
	}

	// Expand env vars in API keys (e.g., "$OPENAI_API_KEY").
	for i := range providers {
		if strings.HasPrefix(providers[i].APIKey, "$") {
			envName := strings.TrimPrefix(providers[i].APIKey, "$")
			providers[i].APIKey = os.Getenv(envName)
		}
	}

	return providers, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func getEnvList(key string) []string {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
