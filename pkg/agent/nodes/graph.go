package nodes

import (
	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/graph"
	"souz.ru/souz-go/pkg/providers"
	"souz.ru/souz-go/pkg/tools"
)

// BuildGraph assembles the full per-turn agent graph:
//
//	classify -> enrich -> llm -> [toolloop -> llm]* -> summarize
//
// The toolloop/llm loop repeats for as long as the model keeps requesting
// tool calls, bounded by the caller's step limit (see graph.Runner.Run).
//
// The returned Definition and start Node are handed to agent.NewExecutor by
// the caller (cmd/souz-agent). Graph assembly lives here rather than in
// pkg/agent because pkg/agent/nodes already imports pkg/agent for
// AgentContext; pkg/agent importing back would be a cycle.
func BuildGraph(provider providers.LLMProvider, registry map[string]tools.Tool) (*graph.Definition, *graph.Node) {
	classify := NewClassify(registry)
	enrich := NewEnrich(nil)
	llm := NewLLM(provider)
	toolLoop := NewToolLoop(registry)
	summarize := NewSummarize(provider)

	def := graph.NewDefinition()
	def.AddEdge(classify, enrich)
	def.AddEdge(enrich, llm)
	def.AddConditionalEdge(llm, hasPendingToolCalls, toolLoop)
	def.AddConditionalEdge(llm, negate(hasPendingToolCalls), summarize)
	def.AddEdge(toolLoop, llm)

	return def, classify
}

func hasPendingToolCalls(out any) bool {
	ac, ok := out.(agent.AgentContext)
	if !ok || len(ac.History) == 0 {
		return false
	}
	last := ac.History[len(ac.History)-1]
	return last.Role == providers.RoleAssistant && len(last.ToolCalls) > 0
}

func negate(cond graph.EdgeCondition) graph.EdgeCondition {
	return func(out any) bool { return !cond(out) }
}
