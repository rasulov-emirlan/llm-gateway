package middleware

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/erasulov/llm-gateway/internal/apikey"
)

// Auth validates the API key from the Authorization header using the key store.
// If store is nil or empty, authentication is disabled (open access).
// On success, the resolved *apikey.APIKey is injected into the request context.
func Auth(store *apikey.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// If no keys configured, skip auth (open access).
			if store == nil || store.Count() == 0 {
				next.ServeHTTP(w, r)
				return
			}

			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if token == "" {
				slog.Warn("missing authorization header", "remote_addr", r.RemoteAddr)
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			key := store.Lookup(token)
			if key == nil {
				slog.Warn("invalid API key", "remote_addr", r.RemoteAddr)
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			// Inject the API key into context for downstream use
			// (rate limiting, usage tracking, etc.)
			ctx := apikey.WithAPIKey(r.Context(), key)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
