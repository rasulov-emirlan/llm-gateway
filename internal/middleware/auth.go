package middleware

import (
	"log/slog"
	"net/http"
	"strings"
)

// Auth validates the API key from the Authorization header.
// If apiKey is empty, authentication is disabled.
func Auth(apiKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if apiKey == "" {
				next.ServeHTTP(w, r)
				return
			}

			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if token == "" || token != apiKey {
				slog.Warn("unauthorized request", "remote_addr", r.RemoteAddr)
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
