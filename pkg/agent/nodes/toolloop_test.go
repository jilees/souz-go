package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/providers"
	"souz.ru/souz-go/pkg/tools"
)

type fakeTool struct {
	name   string
	result string
	err    error
	gotArg map[string]json.RawMessage
}

func (f *fakeTool) Name() string            { return f.name }
func (f *fakeTool) Description() string     { return "fake tool" }
func (f *fakeTool) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (f *fakeTool) Execute(_ context.Context, args map[string]json.RawMessage, _ agent.InvocationMeta) (string, error) {
	f.gotArg = args
	return f.result, f.err
}

var _ tools.Tool = (*fakeTool)(nil)

func TestToolLoop_ExecutesPendingCalls(t *testing.T) {
	weather := &fakeTool{name: "get_weather", result: "22C sunny"}
	registry := map[string]tools.Tool{"get_weather": weather}
	node := NewToolLoop(registry)

	in := agent.AgentContext{
		History: []providers.Message{
			{Role: providers.RoleUser, Content: "weather?"},
			{
				Role: providers.RoleAssistant,
				ToolCalls: []providers.ToolCall{
					{ID: "call_1", Name: "get_weather", Args: json.RawMessage(`{"city":"Moscow"}`)},
				},
			},
		},
	}

	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.(agent.AgentContext)

	if len(got.History) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(got.History), got.History)
	}
	result := got.History[2]
	if result.Role != providers.RoleTool || result.Content != "22C sunny" || result.ToolCallID != "call_1" {
		t.Errorf("unexpected tool result message: %+v", result)
	}
	if string(weather.gotArg["city"]) != `"Moscow"` {
		t.Errorf("expected tool to receive city arg, got %+v", weather.gotArg)
	}
}

func TestToolLoop_UnknownToolProducesErrorResult(t *testing.T) {
	node := NewToolLoop(map[string]tools.Tool{})

	in := agent.AgentContext{
		History: []providers.Message{
			{
				Role: providers.RoleAssistant,
				ToolCalls: []providers.ToolCall{
					{ID: "call_1", Name: "does_not_exist", Args: json.RawMessage(`{}`)},
				},
			},
		},
	}

	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.(agent.AgentContext)

	result := got.History[len(got.History)-1]
	if result.Role != providers.RoleTool || result.ToolCallID != "call_1" {
		t.Fatalf("unexpected result message: %+v", result)
	}
	if result.Content == "" {
		t.Error("expected a non-empty error message for an unknown tool")
	}
}

func TestToolLoop_ToolErrorIsSurfacedAsResult(t *testing.T) {
	failing := &fakeTool{name: "boom", err: errors.New("kaboom")}
	node := NewToolLoop(map[string]tools.Tool{"boom": failing})

	in := agent.AgentContext{
		History: []providers.Message{
			{
				Role: providers.RoleAssistant,
				ToolCalls: []providers.ToolCall{
					{ID: "call_1", Name: "boom", Args: json.RawMessage(`{}`)},
				},
			},
		},
	}

	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.(agent.AgentContext)
	if got.History[len(got.History)-1].Content != "kaboom" {
		t.Errorf("expected tool error surfaced as result content, got %+v", got.History[len(got.History)-1])
	}
}

func TestToolLoop_NoPendingCallsIsNoop(t *testing.T) {
	node := NewToolLoop(map[string]tools.Tool{})

	in := agent.AgentContext{
		History: []providers.Message{
			{Role: providers.RoleAssistant, Content: "no tools needed"},
		},
	}

	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.(agent.AgentContext)
	if len(got.History) != 1 {
		t.Errorf("expected history unchanged, got %+v", got.History)
	}
}
