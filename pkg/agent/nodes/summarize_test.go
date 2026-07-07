package nodes

import (
	"context"
	"strings"
	"testing"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/providers"
)

func TestSummarize_NoopWhenHistorySmall(t *testing.T) {
	provider := &fakeProvider{resp: &providers.ChatResponse{Content: "should not be called"}}
	node := NewSummarize(provider)

	in := agent.AgentContext{
		History:  []providers.Message{{Role: providers.RoleUser, Content: "hi"}},
		Settings: agent.AgentSettings{ContextSize: 100_000},
	}

	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.(agent.AgentContext)
	if len(got.History) != 1 || got.History[0].Content != "hi" {
		t.Errorf("expected history untouched, got %+v", got.History)
	}
}

func TestSummarize_CompactsWhenHistoryTooBig(t *testing.T) {
	provider := &fakeProvider{resp: &providers.ChatResponse{Content: "memory dump text"}}
	node := NewSummarize(provider)

	big := strings.Repeat("x", 1000)
	in := agent.AgentContext{
		History: []providers.Message{
			{Role: providers.RoleUser, Content: big},
			{Role: providers.RoleAssistant, Content: "last reply"},
		},
		Settings: agent.AgentSettings{ContextSize: 100},
	}

	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.(agent.AgentContext)

	if len(got.History) != 2 {
		t.Fatalf("expected 2 messages after compaction, got %d: %+v", len(got.History), got.History)
	}
	if got.History[0].Role != providers.RoleAssistant || !strings.Contains(got.History[0].Content, "memory dump text") {
		t.Errorf("expected summary message first, got %+v", got.History[0])
	}
	if got.History[1].Content != "last reply" {
		t.Errorf("expected the most recent message preserved last, got %+v", got.History[1])
	}
}
