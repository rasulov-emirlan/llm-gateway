package apikey

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStore_AddAndLookup(t *testing.T) {
	store := NewStore()
	store.Add(&APIKey{
		Key:     "sk-test-001",
		Name:    "test key",
		Tier:    TierPro,
		RPM:     60,
		TPM:     100000,
		Enabled: true,
	})

	key := store.Lookup("sk-test-001")
	if key == nil {
		t.Fatal("expected to find key")
	}
	if key.Name != "test key" {
		t.Fatalf("expected name 'test key', got %q", key.Name)
	}
	if key.Tier != TierPro {
		t.Fatalf("expected pro tier, got %s", key.Tier)
	}
}

func TestStore_LookupReturnsNilForUnknown(t *testing.T) {
	store := NewStore()
	if store.Lookup("nonexistent") != nil {
		t.Fatal("expected nil for unknown key")
	}
}

func TestStore_LookupReturnsNilForDisabled(t *testing.T) {
	store := NewStore()
	store.Add(&APIKey{
		Key:     "sk-disabled",
		Enabled: false,
	})

	if store.Lookup("sk-disabled") != nil {
		t.Fatal("expected nil for disabled key")
	}
}

func TestStore_LoadFromFile(t *testing.T) {
	data := `[
		{"key": "sk-file-001", "name": "from file", "tier": "team", "enabled": true},
		{"key": "sk-file-002", "name": "second", "tier": "free", "rpm": 5, "tpm": 5000, "enabled": true}
	]`

	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")
	os.WriteFile(path, []byte(data), 0644)

	store := NewStore()
	if err := store.LoadFromFile(path); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if store.Count() != 2 {
		t.Fatalf("expected 2 keys, got %d", store.Count())
	}

	key := store.Lookup("sk-file-001")
	if key == nil {
		t.Fatal("expected to find sk-file-001")
	}
	// Should have default team limits applied.
	if key.RPM == 0 {
		t.Fatal("expected default RPM to be applied")
	}

	key2 := store.Lookup("sk-file-002")
	if key2 == nil {
		t.Fatal("expected to find sk-file-002")
	}
	if key2.RPM != 5 {
		t.Fatalf("expected RPM 5, got %d", key2.RPM)
	}
}

func TestStore_LoadLegacyKey(t *testing.T) {
	store := NewStore()
	store.LoadLegacyKey("test-key-123")

	key := store.Lookup("test-key-123")
	if key == nil {
		t.Fatal("expected legacy key")
	}
	if key.Tier != TierPro {
		t.Fatalf("expected pro tier, got %s", key.Tier)
	}
}

func TestStore_LoadLegacyKeyEmpty(t *testing.T) {
	store := NewStore()
	store.LoadLegacyKey("")

	if store.Count() != 0 {
		t.Fatal("empty key should not be added")
	}
}

func TestContextRoundtrip(t *testing.T) {
	key := &APIKey{Key: "test", Name: "ctx test"}
	ctx := WithAPIKey(context.Background(), key)

	got := FromContext(ctx)
	if got == nil || got.Key != "test" {
		t.Fatal("context roundtrip failed")
	}
}

func TestFromContext_NilWhenMissing(t *testing.T) {
	if FromContext(context.Background()) != nil {
		t.Fatal("expected nil from empty context")
	}
}

func TestDefaultTierLimits(t *testing.T) {
	tests := []struct {
		tier     Tier
		wantRPM  int
	}{
		{TierFree, 10},
		{TierPro, 60},
		{TierTeam, 300},
	}

	for _, tc := range tests {
		rpm, _, _ := DefaultTierLimits(tc.tier)
		if rpm != tc.wantRPM {
			t.Errorf("tier %s: expected RPM %d, got %d", tc.tier, tc.wantRPM, rpm)
		}
	}
}
