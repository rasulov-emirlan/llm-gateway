// Package admin provides a separate HTTP server for runtime management
// and monitoring. It runs on a different port from the main API and
// exposes provider health, usage stats, and system metrics.
package admin

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/erasulov/llm-gateway/internal/apikey"
	"github.com/erasulov/llm-gateway/internal/provider"
	"github.com/erasulov/llm-gateway/internal/queue"
	"github.com/erasulov/llm-gateway/internal/usage"
)

// Server is the admin API server.
type Server struct {
	registry  *provider.Registry
	keyStore  *apikey.Store
	tracker   usage.Tracker
	admission *queue.AdmissionController
}

// NewServer creates an admin API server.
func NewServer(registry *provider.Registry, keyStore *apikey.Store, tracker usage.Tracker, admission *queue.AdmissionController) *Server {
	return &Server{
		registry:  registry,
		keyStore:  keyStore,
		tracker:   tracker,
		admission: admission,
	}
}

// Handler returns the admin API HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/health", s.handleHealth)
	mux.HandleFunc("GET /admin/providers", s.handleProviders)
	mux.HandleFunc("GET /admin/stats", s.handleStats)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	models, err := s.registry.ListAllModels(ctx)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "degraded",
			"error":  err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"models":      len(models),
		"api_keys":    s.keyStore.Count(),
	})
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	models, _ := s.registry.ListAllModels(r.Context())

	// Group models by provider.
	byProvider := make(map[string][]string)
	for _, m := range models {
		byProvider[m.Provider] = append(byProvider[m.Provider], m.ID)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"providers": byProvider,
	})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	active, queued := s.admission.Stats()

	writeJSON(w, http.StatusOK, map[string]any{
		"admission": map[string]int{
			"active_requests": active,
			"queued_requests": queued,
		},
		"api_keys": s.keyStore.Count(),
	})
}

// ListenAndServe starts the admin server on the given address.
func (s *Server) ListenAndServe(addr string) error {
	slog.Info("starting admin server", "addr", addr)
	return http.ListenAndServe(addr, s.Handler())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
