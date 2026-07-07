# CLAUDE.md — souz-go

Go port of the souz agent for embedded devices (SberBoom Home: 256 MB RAM, 30 MB storage, ARM64 Linux).

## Background

Original KMP project lives at `../souz`. Channel patterns borrowed from `../picoclaw-voice-channel/picoclaw` (reference only — souz-go replaces picoclaw on the device).

## Commands

```bash
# Run agent (local)
go run ./cmd/souz-agent

# Build native binary
go build ./cmd/souz-agent

# Cross-compile for SberBoom (ARM64 Linux, static — the deployment target)
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o souz-agent-arm64 ./cmd/souz-agent

# Test all packages
go test ./...

# Lint
golangci-lint run

# Shrink binary after cross-compile (optional)
upx --best souz-agent-arm64
```

## Module Layout

```
cmd/souz-agent/     ← entry point: DI wiring, graceful shutdown
pkg/
  bus/              ← MessageBus: buffered chan routing (inbound/outbound)
  channels/
    channel.go      ← Channel interface + BaseChannel
    sberboom/       ← WebSocket client connecting to Sber OS bridge
    telegram/       ← Telegram long-polling bot
    mattermost/     ← Mattermost WebSocket real-time
  graph/            ← generic typed graph runner (Node, Definition, Runner)
  agent/
    context.go      ← AgentContext (immutable state snapshot per turn)
    executor.go     ← drives one agent turn: input → output
    nodes/          ← graph nodes: classify, enrich, llm, toolloop, summarize, mcp, skills
  providers/
    types.go        ← LLMProvider interface + ChatRequest/Response/Message DTOs
    anthropic/      ← Anthropic Messages API (streaming)
    openai_compat/  ← OpenAI-compatible (OpenAI, Qwen, AiTunnel, Codex)
  tools/
    tool.go         ← Tool interface
    files/          ← filesystem read/write/search
    web/            ← HTTP fetch + search
    math/           ← calculator
    skills/         ← RunSkillCommand tool
  mcp/              ← MCP client: stdio transport + HTTP+SSE transport
  skills/
    bundle/         ← SkillBundle parser, SKILL.md YAML frontmatter
    validation/     ← structural → static → LLM validation pipeline
    selection/      ← LLM-based skill selector
    registry/       ← filesystem skill registry
  storage/          ← atomic JSON session storage (~/.local/state/souz-go/)
  config/           ← YAML config loader, SecureString
  http/             ← /v1/** HTTP API (KMP client compatible) + SSE event sink
```

## Architecture

**Graph-based agent execution per turn:**
```
classify → inject MCP tools → inject skills → enrich context → LLM call → tool loop → summarize
```

`pkg/graph/` is a pure typed node/graph runner — zero agent/LLM knowledge. `AgentContext` is immutable; nodes return a new copy (value semantics, no shared state).

`pkg/bus/` connects channels to the agent executor via buffered Go channels. Each `Channel` calls `bus.PublishInbound()` on incoming messages and reads `OutboundChan()` for replies.

## HTTP API Compatibility

`/v1/**` routes are intentionally API-compatible with the souz KMP Compose desktop client:
- Same route paths as `BackendHttpRoutes.kt`
- Same JSON field names as `BackendV1Dtos.kt`
- Same SSE event types (MESSAGE_CREATED/DELTA/COMPLETED, EXECUTION_STARTED/FINISHED/FAILED/CANCELLED, TOOL_CALL_STARTED/FINISHED/FAILED, OPTION_REQUESTED/ANSWERED)
- Single-user: `userId = "default"` everywhere; auth headers accepted but ignored.

## Development Constraints

- **Zero CGO.** `CGO_ENABLED=0` in all build commands. Only pure-Go libraries.
- **No PostgreSQL, no Docker sandbox, no voice API, no GigaChat.**
- `AgentContext` passed by value through graph nodes — never take a pointer to it inside a node.
- Channels must not import `pkg/agent`. Bus ↔ agent coupling lives in `cmd/souz-agent/`.
- Binary size target: ≤ 20 MB uncompressed, ≤ 12 MB with UPX.

## State Layout

```
~/.local/state/souz-go/
  sessions/          ← one JSON file per chatId
  skills/            ← installed skill bundles
  skill-validations/ ← cached validation verdicts
  config.yaml        ← default config location (overridden by -config flag)
```

## Plan

See `docs/plan.md` for the full phased implementation plan.
