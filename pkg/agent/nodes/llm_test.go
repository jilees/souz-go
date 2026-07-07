package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/providers"
)

type fakeProvider struct {
	resp       *providers.ChatResponse
	err        error
	gotReq     providers.ChatRequest
	streamText string
}

func (f *fakeProvider) Chat(_ context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	f.gotReq = req
	return f.resp, f.err
}

func (f *fakeProvider) ChatStream(_ context.Context, req providers.ChatRequest, onChunk func(string)) (*providers.ChatResponse, error) {
	f.gotReq = req
	if f.err != nil {
		return nil, f.err
	}
	if f.streamText != "" {
		onChunk(f.streamText)
	}
	return f.resp, nil
}

type fakeEventSink struct {
	deltas []string
	agent.NoopEventSink
}

func (f *fakeEventSink) EmitDelta(delta string) {
	f.deltas = append(f.deltas, delta)
}

func TestLLM_AppendsAssistantMessage(t *testing.T) {
	provider := &fakeProvider{
		resp:       &providers.ChatResponse{Content: "hello there", FinishReason: providers.FinishStop},
		streamText: "hello there",
	}
	sink := &fakeEventSink{}
	node := NewLLM(provider)

	in := agent.AgentContext{
		History:   []providers.Message{{Role: providers.RoleUser, Content: "hi"}},
		EventSink: sink,
	}

	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.(agent.AgentContext)

	if len(got.History) != 2 {
		t.Fatalf("expected 2 history messages, got %d", len(got.History))
	}
	last := got.History[1]
	if last.Role != providers.RoleAssistant || last.Content != "hello there" {
		t.Errorf("unexpected assistant message: %+v", last)
	}
	if len(sink.deltas) != 1 || sink.deltas[0] != "hello there" {
		t.Errorf("expected one streamed delta %q, got %v", "hello there", sink.deltas)
	}
	// input history must not be mutated in place
	if len(in.History) != 1 {
		t.Errorf("input history was mutated: %+v", in.History)
	}
}

func TestLLM_PropagatesToolCalls(t *testing.T) {
	args := json.RawMessage(`{"x":1}`)
	provider := &fakeProvider{
		resp: &providers.ChatResponse{
			ToolCalls:    []providers.ToolCall{{ID: "call_1", Name: "get_weather", Args: args}},
			FinishReason: providers.FinishToolUse,
		},
	}
	node := NewLLM(provider)

	in := agent.AgentContext{History: []providers.Message{{Role: providers.RoleUser, Content: "weather?"}}}
	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.(agent.AgentContext)
	last := got.History[len(got.History)-1]
	if len(last.ToolCalls) != 1 || last.ToolCalls[0].Name != "get_weather" {
		t.Errorf("expected tool call get_weather, got %+v", last.ToolCalls)
	}
}

func TestLLM_ErrorDoesNotAppendMessage(t *testing.T) {
	provider := &fakeProvider{err: errors.New("boom")}
	node := NewLLM(provider)

	in := agent.AgentContext{History: []providers.Message{{Role: providers.RoleUser, Content: "hi"}}}
	out, err := node.Execute(context.Background(), in)
	if err == nil {
		t.Fatal("expected error")
	}
	got := out.(agent.AgentContext)
	if len(got.History) != 1 {
		t.Errorf("expected history unchanged on error, got %+v", got.History)
	}
}

func TestBuildChatRequest_TruncatesToContextSize(t *testing.T) {
	in := agent.AgentContext{
		History: []providers.Message{
			{Role: providers.RoleUser, Content: "1"},
			{Role: providers.RoleAssistant, Content: "2"},
			{Role: providers.RoleUser, Content: "3"},
		},
		Settings: agent.AgentSettings{ContextSize: 2, Model: "m", Temperature: 0.5, MaxTokens: 100},
	}

	req := buildChatRequest(in)
	if len(req.Messages) != 2 {
		t.Fatalf("expected 2 messages after truncation, got %d", len(req.Messages))
	}
	if req.Messages[0].Content != "2" || req.Messages[1].Content != "3" {
		t.Errorf("expected the last 2 messages, got %+v", req.Messages)
	}
	if req.Model != "m" || req.Temperature != 0.5 || req.MaxTokens != 100 {
		t.Errorf("settings not propagated: %+v", req)
	}
}
