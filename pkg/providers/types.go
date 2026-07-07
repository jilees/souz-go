package providers

import (
	"context"
	"encoding/json"
)

// Role identifies a conversation participant.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ContentPart is a single part of a multi-part message (text or tool result).
type ContentPart struct {
	Type       string `json:"type"` // "text" | "tool_result"
	Text       string `json:"text,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Content    string `json:"content,omitempty"` // for type=tool_result
	IsError    bool   `json:"is_error,omitempty"`
}

// Message is a single conversation turn.
type Message struct {
	Role       Role          `json:"role"`
	Content    string        `json:"content,omitempty"`
	Parts      []ContentPart `json:"parts,omitempty"`        // multi-part content (optional)
	ToolCallID string        `json:"tool_call_id,omitempty"` // for role=tool
	Name       string        `json:"name,omitempty"`         // tool name for role=tool
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
}

// ToolCall is an invocation requested by the LLM.
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"` // JSON object matching the tool's schema
}

// ToolDefinition describes a callable tool to the LLM.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"` // JSON Schema for the args object
}

// ChatRequest is the input to Chat and ChatStream.
type ChatRequest struct {
	Model        string           `json:"model"`
	Messages     []Message        `json:"messages"`
	Tools        []ToolDefinition `json:"tools,omitempty"`
	SystemPrompt string           `json:"system,omitempty"`
	Temperature  float64          `json:"temperature,omitempty"`
	MaxTokens    int              `json:"max_tokens,omitempty"`
}

// FinishReason describes why the LLM stopped generating.
type FinishReason string

const (
	FinishStop    FinishReason = "stop"
	FinishToolUse FinishReason = "tool_use"
	FinishLength  FinishReason = "length"
)

// Usage reports token consumption for one request.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ChatResponse is the LLM output.
type ChatResponse struct {
	Content      string       `json:"content"`
	ToolCalls    []ToolCall   `json:"tool_calls,omitempty"`
	Usage        Usage        `json:"usage"`
	FinishReason FinishReason `json:"finish_reason"`
}

// LLMProvider is the common interface for all chat LLM backends.
// Implementations live in pkg/providers/anthropic and pkg/providers/openai_compat.
type LLMProvider interface {
	// Chat performs a non-streaming chat completion.
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)

	// ChatStream performs a streaming chat completion.
	// onChunk is called for each text delta as it arrives.
	// Returns the fully accumulated response when the stream ends.
	ChatStream(ctx context.Context, req ChatRequest, onChunk func(delta string)) (*ChatResponse, error)
}
