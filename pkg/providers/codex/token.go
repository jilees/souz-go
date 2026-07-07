// Package codex implements providers.LLMProvider against OpenAI's Responses
// API using a ChatGPT-subscription OAuth token (device-authorization flow),
// not a plain API key — this is the "codex" provider from the Kotlin
// original's SettingsProvider (codexAccessToken/RefreshToken/AccountId/
// ExpiresAt), ported directly rather than kept behind a JVM sidecar: the
// flow itself turned out to be a plain device-code grant plus form-encoded
// refresh (RFC 8628-ish, no browser redirect, no local callback server),
// well within reach of a small Go client and consistent with this
// project's "no JVM anywhere" premise.
package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Token is a Codex OAuth token set, persisted between runs so the agent
// doesn't need to redo the device-authorization flow on every restart.
type Token struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	AccountID    string `json:"accountId"`
	// ExpiresAt is a Unix timestamp (seconds).
	ExpiresAt int64 `json:"expiresAt"`
}

// TokenStore persists a single Codex Token as a JSON file. It does not
// live in config.yaml: unlike a static provider API key, this value is
// rewritten by the agent itself on every refresh (roughly hourly), and
// config.yaml is meant to stay a stable, hand-edited file.
type TokenStore struct {
	path string
}

// NewTokenStore wraps path (e.g. ~/.local/state/souz-go/codex-token.json).
func NewTokenStore(path string) *TokenStore {
	return &TokenStore{path: path}
}

// Load reads the stored token, or (nil, nil) if none has been saved yet
// (Codex isn't linked — run the device-authorization flow first).
func (s *TokenStore) Load() (*Token, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("codex: read token: %w", err)
	}
	var t Token
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("codex: decode token: %w", err)
	}
	return &t, nil
}

// Save persists tok atomically, creating parent directories as needed.
func (s *TokenStore) Save(tok *Token) error {
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return fmt.Errorf("codex: encode token: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("codex: %w", err)
	}
	if err := writeFileAtomic(s.path, data); err != nil {
		return fmt.Errorf("codex: %w", err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
