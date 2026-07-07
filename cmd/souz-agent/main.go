// Command souz-agent is the entry point: it wires config, providers, tools,
// MCP clients, the agent graph, storage, channels, and the HTTP API
// together, then runs until asked to stop.
package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/agent/nodes"
	"souz.ru/souz-go/pkg/bus"
	"souz.ru/souz-go/pkg/channels"
	"souz.ru/souz-go/pkg/config"
	"souz.ru/souz-go/pkg/graph"
	souzhttp "souz.ru/souz-go/pkg/http"
	"souz.ru/souz-go/pkg/skills/bundle"
	skillsregistry "souz.ru/souz-go/pkg/skills/registry"
	"souz.ru/souz-go/pkg/skills/validation"
	"souz.ru/souz-go/pkg/storage"
)

const agentVersion = "0.1.0"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	configPath := flag.String("config", config.DefaultPath(), "path to config.yaml")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "path", *configPath, "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, cfg); err != nil {
		slog.Error("souz-agent exited with error", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg *config.Config) error {
	slog.Info("souz-agent starting", "provider", cfg.Provider, "model", cfg.Model, "dataDir", cfg.DataDir)

	provider, err := buildProvider(cfg)
	if err != nil {
		return fmt.Errorf("provider: %w", err)
	}

	skillsReg := skillsregistry.New(filepath.Join(cfg.DataDir, "skills"), bundle.DefaultPolicy())
	validationStore := validation.NewStore(filepath.Join(cfg.DataDir, "skill-validations"))

	toolRegistry := buildToolRegistry(cfg, skillsReg, validationStore)

	mcpClients := buildMCPClients(ctx, cfg)
	defer closeMCPClients(mcpClients)

	var skillsCfg nodes.SkillsConfig
	if cfg.Skills.Enabled {
		skillsCfg = nodes.SkillsConfig{
			Provider:        provider,
			Registry:        skillsReg,
			ValidationStore: validationStore,
			Policy:          validation.DefaultPolicy(),
		}
	}

	def, start := nodes.BuildGraph(provider, toolRegistry, mcpClients, skillsCfg)
	executor := agent.NewExecutor(def, start, graph.RetryPolicy{}, 64)

	settings := agent.AgentSettings{
		Model:       cfg.Model,
		Temperature: cfg.Temperature,
		MaxTokens:   cfg.MaxTokens,
		ContextSize: cfg.ContextSize,
	}
	sessionStore := storage.New(filepath.Join(cfg.DataDir, "sessions"))

	messageBus := bus.New()
	defer messageBus.Close()

	channelRegistry := buildChannels(cfg, messageBus)

	var wg sync.WaitGroup

	for name, ch := range channelRegistry {
		wg.Add(1)
		go func(name string, ch channels.Channel) {
			defer wg.Done()
			if err := ch.Start(ctx); err != nil && ctx.Err() == nil {
				slog.Error("channel stopped unexpectedly", "channel", name, "error", err)
			}
		}(name, ch)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		runInboundLoop(ctx, messageBus, executor, sessionStore, settings, cfg.SystemPrompt)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		runOutboundLoop(ctx, messageBus, channelRegistry)
	}()

	var httpServer *souzhttp.Server
	if cfg.HTTP.Enabled {
		httpServer = souzhttp.NewServer(executor, sessionStore, settings, cfg.SystemPrompt)
		wg.Add(1)
		go func() {
			defer wg.Done()
			slog.Info("http server listening", "addr", cfg.HTTP.Addr)
			if err := httpServer.ListenAndServe(cfg.HTTP.Addr); err != nil {
				slog.Error("http server stopped unexpectedly", "error", err)
			}
		}()
	}

	<-ctx.Done()
	slog.Info("souz-agent shutting down")

	if httpServer != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			slog.Warn("http server shutdown error", "error", err)
		}
		shutdownCancel()
	}

	wg.Wait()
	return nil
}

// runInboundLoop drives one agent turn per inbound channel message,
// sequentially — a single embedded agent doesn't need concurrent turns
// across chats, and running them one at a time avoids racing on
// storage.ChatState for the same chat.
func runInboundLoop(ctx context.Context, mb *bus.MessageBus, executor *agent.Executor, store *storage.Store, settings agent.AgentSettings, systemPrompt string) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-mb.InboundChan():
			if !ok {
				return
			}
			handleInbound(ctx, mb, executor, store, settings, systemPrompt, msg)
		}
	}
}

func handleInbound(ctx context.Context, mb *bus.MessageBus, executor *agent.Executor, store *storage.Store, settings agent.AgentSettings, systemPrompt string, msg bus.InboundMessage) {
	chatID := msg.Channel + ":" + msg.ChatID

	cs, err := store.Get(chatID)
	if err != nil {
		slog.Error("failed to load chat state", "chat", chatID, "error", err)
		return
	}
	if cs == nil {
		cs = &storage.ChatState{ChatID: chatID}
	}

	seed := agent.AgentContext{
		Input:        msg.Text,
		History:      cs.History,
		SystemPrompt: systemPrompt,
		Settings:     settings,
		InvocationMeta: agent.InvocationMeta{
			UserID:         "default",
			ConversationID: chatID,
		},
		EventSink: agent.NoopEventSink{},
	}

	result, err := executor.Execute(ctx, seed)
	if err != nil {
		slog.Error("agent turn failed", "chat", chatID, "error", err)
		return
	}

	now := time.Now().UTC()
	cs.Messages = append(cs.Messages,
		storage.Message{ID: newID(), Role: "user", Content: msg.Text, CreatedAt: now},
		storage.Message{ID: newID(), Role: "assistant", Content: result.Output, CreatedAt: now},
	)
	cs.History = result.Context.History
	if err := store.Save(cs); err != nil {
		slog.Error("failed to save chat state", "chat", chatID, "error", err)
	}

	if err := mb.PublishOutbound(ctx, bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Text:    result.Output,
	}); err != nil {
		slog.Error("failed to publish outbound message", "chat", chatID, "error", err)
	}
}

func runOutboundLoop(ctx context.Context, mb *bus.MessageBus, channelRegistry map[string]channels.Channel) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-mb.OutboundChan():
			if !ok {
				return
			}
			ch, found := channelRegistry[msg.Channel]
			if !found {
				slog.Warn("no channel registered for outbound message", "channel", msg.Channel)
				continue
			}
			if err := ch.Send(ctx, msg); err != nil {
				slog.Error("failed to send outbound message", "channel", msg.Channel, "error", err)
			}
		}
	}
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
