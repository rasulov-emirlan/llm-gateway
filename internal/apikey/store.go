package apikey

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// Store manages API key lookup and validation.
// Starts as in-memory with config file loading, designed to be
// extended with database backing later.
type Store struct {
	mu   sync.RWMutex
	keys map[string]*APIKey // key string → APIKey
}

// NewStore creates an empty key store.
func NewStore() *Store {
	return &Store{
		keys: make(map[string]*APIKey),
	}
}

// Add inserts or updates a key in the store.
func (s *Store) Add(key *APIKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[key.Key] = key
}

// Lookup finds a key by its string value.
// Returns nil if not found or disabled.
func (s *Store) Lookup(key string) *APIKey {
	s.mu.RLock()
	defer s.mu.RUnlock()

	k, ok := s.keys[key]
	if !ok || !k.Enabled {
		return nil
	}
	return k
}

// Count returns the number of keys in the store.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.keys)
}

// LoadFromFile loads API keys from a JSON file.
// File format: array of APIKey objects.
func (s *Store) LoadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read keys file: %w", err)
	}

	var keys []APIKey
	if err := json.Unmarshal(data, &keys); err != nil {
		return fmt.Errorf("parse keys file: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range keys {
		k := &keys[i]
		if k.CreatedAt.IsZero() {
			k.CreatedAt = time.Now()
		}
		// Apply default limits if not specified.
		if k.RPM == 0 || k.TPM == 0 {
			rpm, tpm, dailyCap := DefaultTierLimits(k.Tier)
			if k.RPM == 0 {
				k.RPM = rpm
			}
			if k.TPM == 0 {
				k.TPM = tpm
			}
			if k.DailyTokenCap == 0 {
				k.DailyTokenCap = dailyCap
			}
		}
		s.keys[k.Key] = k
	}

	slog.Info("loaded API keys", "count", len(keys), "path", path)
	return nil
}

// LoadLegacyKey creates a single key from the legacy API_KEY env var
// for backwards compatibility. Uses pro-tier defaults.
func (s *Store) LoadLegacyKey(key string) {
	if key == "" {
		return
	}

	rpm, tpm, dailyCap := DefaultTierLimits(TierPro)
	s.Add(&APIKey{
		Key:           key,
		Name:          "legacy-key",
		Tier:          TierPro,
		RPM:           rpm,
		TPM:           tpm,
		DailyTokenCap: dailyCap,
		Enabled:       true,
		CreatedAt:     time.Now(),
	})

	slog.Info("loaded legacy API key as pro tier")
}
