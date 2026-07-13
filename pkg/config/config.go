// Package config loads souz-agent's YAML configuration.
//
// SecureString is a redaction-only wrapper, not at-rest encryption: the
// Kotlin original AES-GCM-encrypts provider keys because it's a
// multi-tenant server storing many users' secrets. souz-go is a single-user
// local process — config.yaml already gets the same protection every other
// local secret file gets (0600 permissions, enforced by Load/Save), and the
// master-key lifecycle that real encryption would need isn't proportional
// to that threat model. SecureString still earns its name by keeping keys
// out of logs (String/GoString redact) and out of accidental %v dumps.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SecureString holds a secret loaded from config.yaml. It never appears in
// logs or default formatting — use Value() to get the actual secret.
type SecureString struct {
	value string
}

// NewSecureString wraps a plaintext secret.
func NewSecureString(s string) SecureString { return SecureString{value: s} }

// Value returns the underlying secret.
func (s SecureString) Value() string { return s.value }

// IsSet reports whether a non-empty secret was provided.
func (s SecureString) IsSet() bool { return s.value != "" }

// String redacts the value; never prints the secret.
func (s SecureString) String() string {
	if s.value == "" {
		return ""
	}
	return "[REDACTED]"
}

// GoString redacts the value in %#v output too.
func (s SecureString) GoString() string { return s.String() }

// MarshalYAML writes the raw secret so config.yaml stays a plain, hand-editable file.
func (s SecureString) MarshalYAML() (any, error) { return s.value, nil }

// UnmarshalYAML reads a plain scalar value.
func (s *SecureString) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		return err
	}
	s.value = raw
	return nil
}

// ProviderConfig configures one LLM backend.
type ProviderConfig struct {
	APIKey  SecureString `yaml:"apiKey"`
	Model   string       `yaml:"model,omitempty"`
	BaseURL string       `yaml:"baseURL,omitempty"` // override; empty uses the provider's default
}

// TelegramConfig configures the Telegram channel.
type TelegramConfig struct {
	Enabled   bool         `yaml:"enabled"`
	Token     SecureString `yaml:"token"`
	AllowFrom []string     `yaml:"allowFrom,omitempty"`
}

// ToolsConfig configures the built-in local tools.
type ToolsConfig struct {
	// FilesRoot sandboxes the files tool; empty disables it (no tool is
	// registered without a root to confine it to).
	FilesRoot  string `yaml:"filesRoot,omitempty"`
	WebEnabled bool   `yaml:"webEnabled"`
}

// HTTPConfig configures the /v1/** API server.
type HTTPConfig struct {
	Enabled bool   `yaml:"enabled"`
	Addr    string `yaml:"addr"`
}

// MCPServerConfig configures one MCP server connection.
type MCPServerConfig struct {
	Name string `yaml:"name"`
	// Transport is "stdio" or "http_sse".
	Transport string `yaml:"transport"`
	// Command/Args are used when Transport is "stdio".
	Command string   `yaml:"command,omitempty"`
	Args    []string `yaml:"args,omitempty"`
	// URL is used when Transport is "http_sse".
	URL string `yaml:"url,omitempty"`
}

// SkillsSettings configures the skills subsystem.
type SkillsSettings struct {
	Enabled bool `yaml:"enabled"`
}

// Config is souz-agent's top-level configuration.
type Config struct {
	// DataDir is the base directory for sessions/skills/skill-validations
	// (~/.local/state/souz-go by default).
	DataDir string `yaml:"dataDir"`

	Provider     string  `yaml:"provider"` // "anthropic" | "openai_compat"
	Model        string  `yaml:"model"`
	Temperature  float64 `yaml:"temperature"`
	ContextSize  int     `yaml:"contextSize"`
	MaxTokens    int     `yaml:"maxTokens"`
	SystemPrompt string  `yaml:"systemPrompt,omitempty"`
	Locale       string  `yaml:"locale,omitempty"`
	TimeZone     string  `yaml:"timeZone,omitempty"`

	Anthropic    ProviderConfig `yaml:"anthropic"`
	OpenAICompat ProviderConfig `yaml:"openaiCompat"`

	Telegram TelegramConfig `yaml:"telegram"`

	Tools ToolsConfig `yaml:"tools"`
	HTTP  HTTPConfig  `yaml:"http"`

	MCP    []MCPServerConfig `yaml:"mcp,omitempty"`
	Skills SkillsSettings    `yaml:"skills"`
}

// defaultSystemPrompt is the fallback used when config.yaml sets no
// systemPrompt. Ported from the Kotlin original's SystemPromptResolver
// (agent/src/main/kotlin/ru/souz/agent/SystemPromptResolver.kt) — souz-go
// takes the tool-use rules variant rather than the KMP backend's generic
// "answer directly" line, since souz-go's graph always runs a tool loop.
const defaultSystemPrompt = `## Правила работы:
1. **Приоритет инструментов:** Если задачу можно решить вызовом функции — ВЫЗЫВАЙ ЕЁ. Никогда не пиши название функции текстом и не присылай примеры кода на Python/Bash, если ты не собираешься их исполнять через инструмент.
2. **Рассуждения (Chain of Thought):** Перед действием кратко проанализируй запрос. Сначала подумай, какой инструмент нужен, затем используй его.
3. **Формат ответа:**
   - Если результат получен: кратко сообщи об успехе.
   - Если ошибка: сообщи суть проблемы и предложи решение.
4. **Работа с файлами:** Будь краток. Не выводи содержимое файлов, если тебя об этом прямо не просили.
5. **Возврат текста:**
   - Если нужно вернуть текст - возвращай в формате Markdown.
   - В Markdown не возвращай таблицы - вместо них возвращай форматированные списки.

## Критически важно:
Твоя задача — ДЕЙСТВОВАТЬ, а не болтать.`

// Default returns a Config with sane defaults for an empty/missing config.yaml.
func Default() *Config {
	return &Config{
		DataDir:      defaultDataDir(),
		Provider:     "anthropic",
		Temperature:  0.7,
		ContextSize:  16_000,
		MaxTokens:    4_096,
		SystemPrompt: defaultSystemPrompt,
		Tools:        ToolsConfig{WebEnabled: true},
		HTTP:         HTTPConfig{Enabled: true, Addr: ":8080"},
		Skills:       SkillsSettings{Enabled: true},
	}
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".souz-go"
	}
	return filepath.Join(home, ".local", "state", "souz-go")
}

// DefaultPath returns the default config.yaml location under DataDir.
func DefaultPath() string {
	return filepath.Join(defaultDataDir(), "config.yaml")
}

// Load reads YAML config from path over Default()'s values. A missing file
// is not an error — a fresh install has no config.yaml yet — and yields
// pure defaults.
func Load(path string) (*Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

// Save writes cfg as YAML to path, creating parent directories as needed,
// with permissions restricted to the owner (config.yaml may contain
// plaintext API keys — see the package doc on SecureString).
func Save(path string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}
