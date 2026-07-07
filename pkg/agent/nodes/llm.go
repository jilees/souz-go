package nodes

import (
	"context"
	"fmt"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/graph"
	"souz.ru/souz-go/pkg/providers"
)

// NewLLM builds the "llm" graph node: it sends the current history to the
// given provider, streams text deltas to ctx.EventSink, and appends the
// resulting assistant message (text and/or tool calls) to the history.
//
// Deciding whether to loop on tool calls is the responsibility of a later
// toolloop node; this node only makes the call and records the response.
func NewLLM(provider providers.LLMProvider) *graph.Node {
	return graph.NewNode("llm", func(ctx context.Context, in agent.AgentContext) (agent.AgentContext, error) {
		sink := in.EventSink
		if sink == nil {
			sink = agent.NoopEventSink{}
		}

		resp, err := provider.ChatStream(ctx, buildChatRequest(in), sink.EmitDelta)
		if err != nil {
			return in, fmt.Errorf("llm node: %w", err)
		}

		assistantMsg := providers.Message{
			Role:      providers.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		}
		return in.WithHistory(assistantMsg), nil
	})
}

func buildChatRequest(in agent.AgentContext) providers.ChatRequest {
	messages := in.History
	if size := in.Settings.ContextSize; size > 0 && len(messages) > size {
		messages = messages[len(messages)-size:]
	}
	return providers.ChatRequest{
		Model:        in.Settings.Model,
		Messages:     messages,
		Tools:        in.ActiveTools,
		SystemPrompt: in.SystemPrompt,
		Temperature:  in.Settings.Temperature,
		MaxTokens:    in.Settings.MaxTokens,
	}
}
