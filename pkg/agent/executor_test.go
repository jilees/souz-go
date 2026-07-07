package agent

import (
	"context"
	"errors"
	"testing"

	"souz.ru/souz-go/pkg/graph"
	"souz.ru/souz-go/pkg/providers"
)

func TestExecutor_ReturnsLastAssistantText(t *testing.T) {
	appendMsg := graph.NewNode("append", func(_ context.Context, in AgentContext) (AgentContext, error) {
		return in.WithHistory(providers.Message{Role: providers.RoleAssistant, Content: "final answer"}), nil
	})
	def := graph.NewDefinition()
	exec := NewExecutor(def, appendMsg, graph.RetryPolicy{}, 0)

	result, err := exec.Execute(context.Background(), AgentContext{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output != "final answer" {
		t.Errorf("expected %q, got %q", "final answer", result.Output)
	}
}

func TestExecutor_PropagatesNodeError(t *testing.T) {
	failing := graph.NewNode("fail", func(_ context.Context, in AgentContext) (AgentContext, error) {
		return in, errors.New("boom")
	})
	def := graph.NewDefinition()
	exec := NewExecutor(def, failing, graph.RetryPolicy{}, 0)

	if _, err := exec.Execute(context.Background(), AgentContext{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestLastAssistantText_PrefersNonBlank(t *testing.T) {
	history := []providers.Message{
		{Role: providers.RoleAssistant, Content: "first"},
		{Role: providers.RoleUser, Content: "ignored"},
		{Role: providers.RoleAssistant, Content: ""},
	}
	if got := lastAssistantText(history); got != "first" {
		t.Errorf("expected %q, got %q", "first", got)
	}
}
