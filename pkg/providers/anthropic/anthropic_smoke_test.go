package anthropic

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"souz.ru/souz-go/pkg/providers"
)

// TestChatSmoke hits the real Anthropic API. Skipped unless ANTHROPIC_API_KEY is set.
func TestChatSmoke(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping smoke test")
	}

	p := &Provider{APIKey: apiKey}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := p.Chat(ctx, providers.ChatRequest{
		Messages: []providers.Message{
			{Role: providers.RoleUser, Content: "Reply with exactly the word: pong"},
		},
		MaxTokens: 16,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(strings.ToLower(resp.Content), "pong") {
		t.Errorf("expected response to contain %q, got %q", "pong", resp.Content)
	}
}

// TestChatStreamSmoke hits the real Anthropic API with streaming. Skipped unless ANTHROPIC_API_KEY is set.
func TestChatStreamSmoke(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping smoke test")
	}

	p := &Provider{APIKey: apiKey}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var streamed strings.Builder
	resp, err := p.ChatStream(ctx, providers.ChatRequest{
		Messages: []providers.Message{
			{Role: providers.RoleUser, Content: "Reply with exactly the word: pong"},
		},
		MaxTokens: 16,
	}, func(delta string) {
		streamed.WriteString(delta)
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if streamed.Len() == 0 {
		t.Error("expected at least one streamed chunk")
	}
	if resp.Content != streamed.String() {
		t.Errorf("accumulated content %q does not match streamed chunks %q", resp.Content, streamed.String())
	}
}
