package codex

import (
	"encoding/json"
	"testing"

	"souz.ru/souz-go/pkg/providers"
)

func TestBuildResponsesRequest_SystemPromptBecomesInstructions(t *testing.T) {
	req := providers.ChatRequest{
		Model:        "gpt-5-codex",
		SystemPrompt: "You are helpful.",
		Messages:     []providers.Message{{Role: providers.RoleUser, Content: "hi"}},
	}
	out := buildResponsesRequest(req)
	if out.Instructions != "You are helpful." {
		t.Errorf("Instructions = %q", out.Instructions)
	}
	if !out.Stream || out.Store {
		t.Errorf("expected Stream=true, Store=false, got %+v", out)
	}
	if len(out.Input) != 1 || out.Input[0]["type"] != "message" || out.Input[0]["role"] != "user" {
		t.Errorf("unexpected input: %+v", out.Input)
	}
}

func TestBuildInputItems_AssistantWithToolCallsAndText(t *testing.T) {
	messages := []providers.Message{
		{Role: providers.RoleSystem, Content: "ignored, goes to instructions"},
		{Role: providers.RoleUser, Content: "weather?"},
		{
			Role:    providers.RoleAssistant,
			Content: "let me check",
			ToolCalls: []providers.ToolCall{
				{ID: "call_1", Name: "get_weather", Args: json.RawMessage(`{"city":"Tallinn"}`)},
			},
		},
		{Role: providers.RoleTool, Content: "22C sunny", ToolCallID: "call_1"},
	}

	items := buildInputItems(messages)
	if len(items) != 4 {
		t.Fatalf("expected 4 items (system dropped), got %d: %+v", len(items), items)
	}
	if items[0]["type"] != "message" || items[0]["role"] != "user" {
		t.Errorf("unexpected item[0]: %+v", items[0])
	}
	if items[1]["type"] != "message" || items[1]["role"] != "assistant" || items[1]["content"] != "let me check" {
		t.Errorf("unexpected item[1]: %+v", items[1])
	}
	if items[2]["type"] != "function_call" || items[2]["call_id"] != "call_1" || items[2]["name"] != "get_weather" {
		t.Errorf("unexpected item[2]: %+v", items[2])
	}
	if items[3]["type"] != "function_call_output" || items[3]["call_id"] != "call_1" || items[3]["output"] != "22C sunny" {
		t.Errorf("unexpected item[3]: %+v", items[3])
	}
}

func TestBuildInputItems_AssistantToolCallOnlyOmitsEmptyMessage(t *testing.T) {
	messages := []providers.Message{
		{
			Role:      providers.RoleAssistant,
			ToolCalls: []providers.ToolCall{{ID: "c1", Name: "fn", Args: json.RawMessage(`{}`)}},
		},
	}
	items := buildInputItems(messages)
	if len(items) != 1 || items[0]["type"] != "function_call" {
		t.Fatalf("expected exactly one function_call item, got %+v", items)
	}
}

func TestBuildResponsesRequest_ToolsMapDirectlyFromSchema(t *testing.T) {
	req := providers.ChatRequest{
		Tools: []providers.ToolDefinition{
			{Name: "calc", Description: "does math", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}
	out := buildResponsesRequest(req)
	if len(out.Tools) != 1 || out.Tools[0].Type != "function" || out.Tools[0].Name != "calc" {
		t.Fatalf("unexpected tools: %+v", out.Tools)
	}
	if string(out.Tools[0].Parameters) != `{"type":"object"}` {
		t.Errorf("unexpected parameters: %s", out.Tools[0].Parameters)
	}
}
