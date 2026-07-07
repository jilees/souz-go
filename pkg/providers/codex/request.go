package codex

import (
	"encoding/json"

	"souz.ru/souz-go/pkg/providers"
)

// responsesRequest is OpenAI's Responses API request shape — distinct from
// the Chat Completions shape openai_compat targets, so it isn't shared with
// that package.
type responsesRequest struct {
	Model        string           `json:"model"`
	Input        []map[string]any `json:"input"`
	Instructions string           `json:"instructions,omitempty"`
	Store        bool             `json:"store"`
	Stream       bool             `json:"stream"`
	Tools        []responsesTool  `json:"tools,omitempty"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

func buildResponsesRequest(req providers.ChatRequest) responsesRequest {
	out := responsesRequest{
		Model:        req.Model,
		Instructions: req.SystemPrompt,
		Store:        false,
		Stream:       true,
		Input:        buildInputItems(req.Messages),
	}
	if len(req.Tools) > 0 {
		out.Tools = make([]responsesTool, len(req.Tools))
		for i, t := range req.Tools {
			out.Tools[i] = responsesTool{Type: "function", Name: t.Name, Description: t.Description, Parameters: t.InputSchema}
		}
	}
	return out
}

// buildInputItems maps the Go DTO's structured Message/ToolCalls directly
// into Responses API items — simpler than the original, which has to
// reconstruct a tool call by parsing it back out of a JSON-in-a-string
// content field, an artifact of Kotlin's LLMRequest.Message not modeling
// tool calls as a first-class field the way providers.Message does.
func buildInputItems(messages []providers.Message) []map[string]any {
	var items []map[string]any
	for _, m := range messages {
		switch m.Role {
		case providers.RoleSystem:
			continue // carried via top-level "instructions" instead
		case providers.RoleUser:
			items = append(items, map[string]any{"type": "message", "role": "user", "content": m.Content})
		case providers.RoleAssistant:
			if m.Content != "" {
				items = append(items, map[string]any{"type": "message", "role": "assistant", "content": m.Content})
			}
			for _, tc := range m.ToolCalls {
				args := string(tc.Args)
				if args == "" {
					args = "{}"
				}
				items = append(items, map[string]any{
					"type":      "function_call",
					"call_id":   tc.ID,
					"name":      tc.Name,
					"arguments": args,
				})
			}
		case providers.RoleTool:
			items = append(items, map[string]any{
				"type":    "function_call_output",
				"call_id": m.ToolCallID,
				"output":  m.Content,
			})
		}
	}
	return items
}
