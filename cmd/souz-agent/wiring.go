package main

import (
	"context"
	"fmt"
	"log/slog"

	"souz.ru/souz-go/pkg/bus"
	"souz.ru/souz-go/pkg/channels"
	"souz.ru/souz-go/pkg/channels/telegram"
	"souz.ru/souz-go/pkg/config"
	"souz.ru/souz-go/pkg/mcp"
	"souz.ru/souz-go/pkg/providers"
	"souz.ru/souz-go/pkg/providers/anthropic"
	"souz.ru/souz-go/pkg/providers/openai_compat"
	skillsregistry "souz.ru/souz-go/pkg/skills/registry"
	"souz.ru/souz-go/pkg/skills/validation"
	"souz.ru/souz-go/pkg/tools"
	"souz.ru/souz-go/pkg/tools/files"
	"souz.ru/souz-go/pkg/tools/math"
	skillstool "souz.ru/souz-go/pkg/tools/skills"
	"souz.ru/souz-go/pkg/tools/web"
)

// buildProvider constructs the configured LLM backend. cfg.Provider selects
// between the two clients built in Phase 1; there is no default fallback —
// an unrecognized value is a config error, not silently ignored.
func buildProvider(cfg *config.Config) (providers.LLMProvider, error) {
	switch cfg.Provider {
	case "anthropic":
		return &anthropic.Provider{
			APIKey:  cfg.Anthropic.APIKey.Value(),
			BaseURL: cfg.Anthropic.BaseURL,
		}, nil
	case "openai_compat":
		return &openai_compat.Provider{
			APIKey:  cfg.OpenAICompat.APIKey.Value(),
			BaseURL: cfg.OpenAICompat.BaseURL,
		}, nil
	default:
		return nil, fmt.Errorf("unknown provider %q (expected \"anthropic\" or \"openai_compat\")", cfg.Provider)
	}
}

// buildToolRegistry assembles every local tools.Tool the config enables.
// A tool whose own setup fails (e.g. an unusable files sandbox root) is
// skipped with a warning rather than aborting startup — one broken tool
// shouldn't take down the whole agent.
func buildToolRegistry(cfg *config.Config, skillsReg *skillsregistry.Registry, validationStore *validation.Store) map[string]tools.Tool {
	all := []tools.Tool{math.Calculator{}}

	if cfg.Tools.WebEnabled {
		all = append(all, web.New(web.Config{})...)
	}

	if cfg.Tools.FilesRoot != "" {
		fileTools, err := files.New(cfg.Tools.FilesRoot)
		if err != nil {
			slog.Warn("files tool: failed to initialize, skipping", "root", cfg.Tools.FilesRoot, "error", err)
		} else {
			all = append(all, fileTools...)
		}
	}

	if cfg.Skills.Enabled {
		all = append(all, skillstool.New(skillsReg, validationStore, validation.DefaultPolicy().Version))
	}

	return tools.NewRegistry(all)
}

// buildMCPClients connects to every configured MCP server. A server that
// fails to start or complete the initialize handshake is skipped with a
// warning — MCP servers are optional enrichments, not required for the
// agent to function.
func buildMCPClients(ctx context.Context, cfg *config.Config) map[string]*mcp.Client {
	clients := make(map[string]*mcp.Client)
	for _, sc := range cfg.MCP {
		var transport mcp.Transport
		switch sc.Transport {
		case "stdio":
			transport = mcp.NewStdioTransport(sc.Command, sc.Args...)
		case "http_sse":
			transport = mcp.NewHTTPSSETransport(sc.URL)
		default:
			slog.Warn("mcp: unknown transport, skipping server", "server", sc.Name, "transport", sc.Transport)
			continue
		}

		client := mcp.NewClient(transport)
		if err := client.Start(ctx); err != nil {
			slog.Warn("mcp: failed to start server, skipping", "server", sc.Name, "error", err)
			continue
		}
		if _, err := client.Initialize(ctx, "souz-agent", agentVersion); err != nil {
			slog.Warn("mcp: failed to initialize server, skipping", "server", sc.Name, "error", err)
			_ = client.Close()
			continue
		}

		clients[sc.Name] = client
		slog.Info("mcp: connected", "server", sc.Name, "transport", sc.Transport)
	}
	return clients
}

func closeMCPClients(clients map[string]*mcp.Client) {
	for name, c := range clients {
		if err := c.Close(); err != nil {
			slog.Warn("mcp: failed to close server", "server", name, "error", err)
		}
	}
}

// buildChannels wires up every channel the config enables. Only Telegram is
// implemented so far (sberboom/mattermost remain future work); an empty map
// is a valid outcome — the HTTP API still works with no channels running.
func buildChannels(cfg *config.Config, mb *bus.MessageBus) map[string]channels.Channel {
	result := make(map[string]channels.Channel)
	if cfg.Telegram.Enabled {
		ch := telegram.New(telegram.Config{
			Token:     cfg.Telegram.Token.Value(),
			AllowFrom: cfg.Telegram.AllowFrom,
		}, mb)
		result[ch.Name()] = ch
	}
	return result
}
