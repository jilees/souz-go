package nodes

import (
	"context"
	"strings"
	"testing"
	"time"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/providers"
)

func fixedNow(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func TestEnrich_InjectsContextAndInput(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 30, 0, 0, time.UTC)
	node := NewEnrich(fixedNow(now))

	in := agent.AgentContext{
		Input:          "hello there",
		InvocationMeta: agent.InvocationMeta{TimeZone: "UTC", Locale: "en-US"},
	}

	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.(agent.AgentContext)

	if len(got.History) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(got.History), got.History)
	}
	if !strings.Contains(got.History[0].Content, contextMarker) {
		t.Errorf("expected first message to contain context marker, got %q", got.History[0].Content)
	}
	if !strings.Contains(got.History[0].Content, "2026-07-07") {
		t.Errorf("expected context message to contain the date, got %q", got.History[0].Content)
	}
	if got.History[1].Role != providers.RoleUser || got.History[1].Content != "hello there" {
		t.Errorf("expected trailing user message with input, got %+v", got.History[1])
	}
}

func TestEnrich_StripsStaleContext(t *testing.T) {
	node := NewEnrich(fixedNow(time.Now()))

	in := agent.AgentContext{
		Input: "new question",
		History: []providers.Message{
			{Role: providers.RoleUser, Content: "<context>\nstale\n</context>"},
			{Role: providers.RoleUser, Content: "old question"},
			{Role: providers.RoleAssistant, Content: "old answer"},
		},
	}

	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.(agent.AgentContext)

	for _, m := range got.History[:len(got.History)-2] {
		if strings.Contains(m.Content, "stale") {
			t.Errorf("stale context message was not stripped: %+v", got.History)
		}
	}
	if got.History[len(got.History)-1].Content != "new question" {
		t.Errorf("expected new input as last message, got %+v", got.History)
	}
}

func TestEnrich_BlankInputSkipsInjection(t *testing.T) {
	node := NewEnrich(fixedNow(time.Now()))

	in := agent.AgentContext{
		History: []providers.Message{{Role: providers.RoleUser, Content: "kept"}},
	}

	out, err := node.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.(agent.AgentContext)

	if len(got.History) != 1 || got.History[0].Content != "kept" {
		t.Errorf("expected history untouched aside from context stripping, got %+v", got.History)
	}
}
