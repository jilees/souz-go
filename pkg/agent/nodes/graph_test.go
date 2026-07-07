package nodes

import (
	"context"
	"encoding/json"
	"testing"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/graph"
	"souz.ru/souz-go/pkg/providers"
	"souz.ru/souz-go/pkg/tools"
)

// sequenceProvider returns its canned responses in order, repeating the last
// one once exhausted, so a test can script a tool-call turn followed by a
// final-answer turn.
type sequenceProvider struct {
	responses []*providers.ChatResponse
	calls     int
}

func (s *sequenceProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	return s.next(), nil
}

func (s *sequenceProvider) ChatStream(_ context.Context, _ providers.ChatRequest, onChunk func(string)) (*providers.ChatResponse, error) {
	r := s.next()
	if r.Content != "" {
		onChunk(r.Content)
	}
	return r, nil
}

func (s *sequenceProvider) next() *providers.ChatResponse {
	r := s.responses[s.calls]
	if s.calls < len(s.responses)-1 {
		s.calls++
	}
	return r
}

func TestBuildGraph_ToolCallThenFinalAnswer(t *testing.T) {
	provider := &sequenceProvider{responses: []*providers.ChatResponse{
		{
			ToolCalls:    []providers.ToolCall{{ID: "call_1", Name: "get_weather", Args: json.RawMessage(`{"city":"Moscow"}`)}},
			FinishReason: providers.FinishToolUse,
		},
		{Content: "It's sunny.", FinishReason: providers.FinishStop},
	}}
	registry := map[string]tools.Tool{"get_weather": &fakeTool{name: "get_weather", result: "22C"}}

	def, start := BuildGraph(provider, registry, nil, SkillsConfig{})
	runner := &graph.Runner{}

	sink := &fakeEventSink{}
	seed := agent.AgentContext{
		Input:     "what's the weather",
		Settings:  agent.AgentSettings{Model: "test-model", ContextSize: 100_000},
		EventSink: sink,
	}

	out, err := runner.Run(context.Background(), start, seed, def, 10, graph.RetryPolicy{}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.(agent.AgentContext)

	if len(got.History) != 5 {
		t.Fatalf("expected 5 messages, got %d: %+v", len(got.History), got.History)
	}
	if got.History[2].Role != providers.RoleAssistant || len(got.History[2].ToolCalls) != 1 {
		t.Errorf("expected tool-call assistant message at index 2, got %+v", got.History[2])
	}
	toolResult := got.History[3]
	if toolResult.Role != providers.RoleTool || toolResult.Content != "22C" || toolResult.ToolCallID != "call_1" {
		t.Errorf("unexpected tool result message: %+v", toolResult)
	}
	final := got.History[4]
	if final.Role != providers.RoleAssistant || final.Content != "It's sunny." {
		t.Errorf("expected final assistant answer, got %+v", final)
	}
	if len(sink.deltas) != 1 || sink.deltas[0] != "It's sunny." {
		t.Errorf("expected the final answer streamed as a delta, got %v", sink.deltas)
	}
}
