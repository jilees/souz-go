package nodes

import (
	"context"
	"fmt"
	"math"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/graph"
	"souz.ru/souz-go/pkg/providers"
)

const (
	summarizeThreshold  = 0.8
	approxCharsPerToken = 4.0
)

const summarizationPrompt = `You are the memory-management module for an autonomous AI agent.
The current session is full; produce a compact "save point" memory dump so work can continue in a fresh context.
Discard small talk and keep only the facts needed to continue: the overall goal, files/paths touched, tools used,
steps completed, any hard constraints the user gave, and the immediate next step.`

const summarizationPrefix = "Previous session was compacted. Resume from this memory dump:"

// NewSummarize builds the "summarize" graph node: when the estimated token
// count of the conversation approaches Settings.ContextSize, it asks the LLM
// to compact the history into a memory dump and replaces the history with
// that summary plus the most recent message. Otherwise it is a no-op.
func NewSummarize(provider providers.LLMProvider) *graph.Node {
	return graph.NewNode("summarize", func(ctx context.Context, in agent.AgentContext) (agent.AgentContext, error) {
		if !historyIsTooBig(in) {
			return in, nil
		}

		conversation := make([]providers.Message, len(in.History), len(in.History)+1)
		copy(conversation, in.History)
		conversation = append(conversation, providers.Message{Role: providers.RoleUser, Content: summarizationPrompt})

		resp, err := provider.Chat(ctx, providers.ChatRequest{
			Model:        in.Settings.Model,
			Messages:     conversation,
			SystemPrompt: in.SystemPrompt,
			Temperature:  in.Settings.Temperature,
			MaxTokens:    in.Settings.MaxTokens,
		})
		if err != nil {
			return in, fmt.Errorf("summarize node: %w", err)
		}

		summaryMsg := providers.Message{
			Role:    providers.RoleAssistant,
			Content: summarizationPrefix + "\n\n" + resp.Content,
		}

		out := in
		if len(in.History) > 0 {
			out.History = []providers.Message{summaryMsg, in.History[len(in.History)-1]}
		} else {
			out.History = []providers.Message{summaryMsg}
		}
		return out, nil
	})
}

func historyIsTooBig(in agent.AgentContext) bool {
	if in.Settings.ContextSize <= 0 {
		return false
	}
	estimated := estimateTokens(in.SystemPrompt)
	for _, m := range in.History {
		estimated += estimateTokens(m.Content)
	}
	return float64(estimated) >= float64(in.Settings.ContextSize)*summarizeThreshold
}

func estimateTokens(s string) int {
	return int(math.Ceil(float64(len(s)) / approxCharsPerToken))
}
