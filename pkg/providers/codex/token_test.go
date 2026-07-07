package codex

import (
	"path/filepath"
	"testing"
)

func TestTokenStore_SaveAndLoadRoundTrips(t *testing.T) {
	store := NewTokenStore(filepath.Join(t.TempDir(), "codex-token.json"))
	tok := &Token{AccessToken: "at", RefreshToken: "rt", AccountID: "acct-1", ExpiresAt: 1234567890}

	if err := store.Save(tok); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil || *got != *tok {
		t.Errorf("Load() = %+v, want %+v", got, tok)
	}
}

func TestTokenStore_LoadMissingReturnsNilNil(t *testing.T) {
	store := NewTokenStore(filepath.Join(t.TempDir(), "does-not-exist.json"))
	got, err := store.Load()
	if err != nil || got != nil {
		t.Fatalf("Load() = %+v, %v, want nil, nil", got, err)
	}
}

func TestTokenStore_SaveCreatesParentDirs(t *testing.T) {
	store := NewTokenStore(filepath.Join(t.TempDir(), "nested", "dir", "codex-token.json"))
	if err := store.Save(&Token{AccessToken: "at"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load()
	if err != nil || got == nil || got.AccessToken != "at" {
		t.Fatalf("Load() = %+v, %v", got, err)
	}
}
