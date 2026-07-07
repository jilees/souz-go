package nodes

import (
	"context"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/graph"
	"souz.ru/souz-go/pkg/tools"
)

// NewClassify builds the "classify" graph node. The Kotlin original narrows
// AgentContext.ActiveTools by LLM-classified intent against a tool category
// catalog, choosing a subset per turn; that routing intelligence needs the
// skills/MCP catalog built in Phase 4. Until then this node advertises every
// registered tool on every turn (no narrowing) — enough to make registry
// tools actually reachable by the LLM.
func NewClassify(registry map[string]tools.Tool) *graph.Node {
	definitions := tools.ToDefinitions(registry)
	return graph.NewNode("classify", func(_ context.Context, in agent.AgentContext) (agent.AgentContext, error) {
		return in.WithTools(definitions), nil
	})
}
