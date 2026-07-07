package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_MissingFileReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Provider != "anthropic" || cfg.ContextSize != 16_000 {
		t.Errorf("expected defaults, got %+v", cfg)
	}
}

func TestLoad_OverridesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `
provider: openai_compat
model: gpt-5-nano
temperature: 0.2
anthropic:
  apiKey: sk-ant-secret
telegram:
  enabled: true
  token: bot-token-123
  allowFrom: ["42"]
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Provider != "openai_compat" || cfg.Model != "gpt-5-nano" || cfg.Temperature != 0.2 {
		t.Errorf("unexpected overrides: %+v", cfg)
	}
	if cfg.Anthropic.APIKey.Value() != "sk-ant-secret" {
		t.Errorf("expected apiKey loaded, got %q", cfg.Anthropic.APIKey.Value())
	}
	if !cfg.Telegram.Enabled || cfg.Telegram.Token.Value() != "bot-token-123" || len(cfg.Telegram.AllowFrom) != 1 {
		t.Errorf("unexpected telegram config: %+v", cfg.Telegram)
	}
	// Fields not present in the YAML keep their defaults.
	if cfg.HTTP.Addr != ":8080" {
		t.Errorf("expected default HTTP addr preserved, got %q", cfg.HTTP.Addr)
	}
}

func TestSaveAndLoad_RoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := Default()
	cfg.Anthropic.APIKey = NewSecureString("sk-round-trip")
	cfg.Model = "claude-haiku-4-5-20251001"

	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected 0600 permissions, got %v", info.Mode().Perm())
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Model != cfg.Model || got.Anthropic.APIKey.Value() != "sk-round-trip" {
		t.Errorf("round trip mismatch: %+v", got)
	}
}

func TestSecureString_RedactsInFormatting(t *testing.T) {
	s := NewSecureString("super-secret-key")
	if strings.Contains(s.String(), "super-secret-key") {
		t.Error("String() leaked the secret")
	}
	if strings.Contains(fmt.Sprintf("%v", s), "super-secret-key") {
		t.Error("percent-v formatting leaked the secret")
	}
	if s.Value() != "super-secret-key" {
		t.Errorf("Value() = %q, want the actual secret", s.Value())
	}
}

func TestSecureString_EmptyStringsToEmpty(t *testing.T) {
	var s SecureString
	if s.IsSet() {
		t.Error("expected zero-value SecureString to be unset")
	}
	if s.String() != "" {
		t.Errorf("expected empty String() for unset secret, got %q", s.String())
	}
}
