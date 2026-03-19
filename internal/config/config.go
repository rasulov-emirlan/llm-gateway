package config

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port           string
	OllamaURL      string
	APIKey         string
	LogLevel       slog.Level
	RateLimit      float64       // requests per second per client
	RateBurst      int           // max burst size
	FallbackModels []string      // ordered fallback model list
	RedisURL       string        // empty = cache disabled
	CacheTTL       time.Duration // cache entry TTL
	OTelEndpoint   string        // OTLP gRPC endpoint, empty = metrics disabled
}

func Load() *Config {
	return &Config{
		Port:           getEnv("PORT", "8080"),
		OllamaURL:      getEnv("OLLAMA_URL", "http://localhost:11434"),
		APIKey:         getEnv("API_KEY", ""),
		LogLevel:       parseLogLevel(getEnv("LOG_LEVEL", "info")),
		RateLimit:      getEnvFloat("RATE_LIMIT", 10),
		RateBurst:      getEnvInt("RATE_BURST", 20),
		FallbackModels: getEnvList("FALLBACK_MODELS"),
		RedisURL:       getEnv("REDIS_URL", ""),
		CacheTTL:       getEnvDuration("CACHE_TTL", 5*time.Minute),
		OTelEndpoint:   getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
	}
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
