package nodes

import (
	"context"
	"sort"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/graph"
	"souz.ru/souz-go/pkg/mcp"
	"souz.ru/souz-go/pkg/providers"
)

// NewMCP builds the "mcp" graph node: it lists tools from each already-
// connected MCP client (Start/Initialize already done by the caller) and
// appends their definitions to ctx.ActiveTools, so the model can call them
// this turn.
//
// Discovery runs fresh every turn — a local stdio/SSE round trip is cheap
// next to an LLM call — rather than being cached, so a server's tool
// catalog can change without restarting the agent. A client that fails to
// respond is skipped for this turn rather than failing it, matching the
// rest of the pipeline's fail-open philosophy for optional enrichments.
//
// Execution, not just advertisement, needs routing too: tool names are
// namespaced "serverName.toolName" (see mcpToolName) so toolloop's
// dispatcher (toolloop.go) can tell an MCP-backed call from a local one and
// route it to the right client via the same mcpClients map.
func NewMCP(clients map[string]*mcp.Client) *graph.Node {
	return graph.NewNode("mcp", func(ctx context.Context, in agent.AgentContext) (agent.AgentContext, error) {
		if len(clients) == 0 {
			return in, nil
		}

		names := make([]string, 0, len(clients))
		for name := range clients {
			names = append(names, name)
		}
		sort.Strings(names)

		var defs []providers.ToolDefinition
		for _, name := range names {
			toolInfos, err := clients[name].ListTools(ctx)
			if err != nil {
				continue
			}
			for _, ti := range toolInfos {
				defs = append(defs, providers.ToolDefinition{
					Name:        mcpToolName(name, ti.Name),
					Description: ti.Description,
					InputSchema: ti.InputSchema,
				})
			}
		}
		if len(defs) == 0 {
			return in, nil
		}

		merged := make([]providers.ToolDefinition, 0, len(in.ActiveTools)+len(defs))
		merged = append(merged, in.ActiveTools...)
		merged = append(merged, defs...)
		return in.WithTools(merged), nil
	})
}

func mcpToolName(serverName, toolName string) string {
	return serverName + "." + toolName
}
