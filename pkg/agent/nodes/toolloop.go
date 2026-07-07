package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/graph"
	"souz.ru/souz-go/pkg/mcp"
	"souz.ru/souz-go/pkg/providers"
	"souz.ru/souz-go/pkg/tools"
)

// NewToolLoop builds the "toolloop" graph node: it executes every tool call
// requested by the most recent assistant message and appends one RoleTool
// result message per call, keyed by ToolCallID so the provider can match
// results back to requests. Looping back to the llm node to let the model
// see the results is expressed at the graph-topology level (see
// BuildGraph), not here.
//
// A call is dispatched to registry first; if its name isn't there but looks
// like "serverName.toolName" for a server in mcpClients, it's routed to
// that MCP client instead (see the "mcp" node, which is what put
// "serverName.toolName" definitions in front of the model in the first
// place).
func NewToolLoop(registry map[string]tools.Tool, mcpClients map[string]*mcp.Client) *graph.Node {
	return graph.NewNode("toolloop", func(ctx context.Context, in agent.AgentContext) (agent.AgentContext, error) {
		calls := lastToolCalls(in.History)
		if len(calls) == 0 {
			return in, nil
		}

		sink := in.EventSink
		if sink == nil {
			sink = agent.NoopEventSink{}
		}

		results := make([]providers.Message, len(calls))
		for i, call := range calls {
			results[i] = providers.Message{
				Role:       providers.RoleTool,
				Content:    executeTool(ctx, registry, mcpClients, call, in.InvocationMeta, sink),
				ToolCallID: call.ID,
				Name:       call.Name,
			}
		}
		return in.WithHistory(results...), nil
	})
}

func lastToolCalls(history []providers.Message) []providers.ToolCall {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == providers.RoleAssistant {
			return history[i].ToolCalls
		}
	}
	return nil
}

func executeTool(ctx context.Context, registry map[string]tools.Tool, mcpClients map[string]*mcp.Client, call providers.ToolCall, meta agent.InvocationMeta, sink agent.EventSink) string {
	sink.EmitToolCall(call.Name, string(call.Args))

	if tool, ok := registry[call.Name]; ok {
		return executeLocalTool(ctx, tool, call, meta, sink)
	}

	if serverName, toolName, ok := splitMCPToolName(call.Name); ok {
		if client, ok := mcpClients[serverName]; ok {
			return executeMCPTool(ctx, client, toolName, call, sink)
		}
	}

	msg := fmt.Sprintf("no such tool: %s", call.Name)
	sink.EmitToolResult(call.Name, msg, true)
	return msg
}

func executeLocalTool(ctx context.Context, tool tools.Tool, call providers.ToolCall, meta agent.InvocationMeta, sink agent.EventSink) string {
	args, err := parseToolArgs(call.Args)
	if err != nil {
		msg := fmt.Sprintf("invalid arguments: %v", err)
		sink.EmitToolResult(call.Name, msg, true)
		return msg
	}

	result, err := tool.Execute(ctx, args, meta)
	if err != nil {
		sink.EmitToolResult(call.Name, err.Error(), true)
		return err.Error()
	}
	sink.EmitToolResult(call.Name, result, false)
	return result
}

func executeMCPTool(ctx context.Context, client *mcp.Client, toolName string, call providers.ToolCall, sink agent.EventSink) string {
	text, isError, err := client.CallTool(ctx, toolName, call.Args)
	if err != nil {
		sink.EmitToolResult(call.Name, err.Error(), true)
		return err.Error()
	}
	sink.EmitToolResult(call.Name, text, isError)
	return text
}

// splitMCPToolName splits a "serverName.toolName" advertised tool name (see
// mcpToolName in mcp.go) back into its parts.
func splitMCPToolName(name string) (server, tool string, ok bool) {
	idx := strings.IndexByte(name, '.')
	if idx <= 0 || idx == len(name)-1 {
		return "", "", false
	}
	return name[:idx], name[idx+1:], true
}

func parseToolArgs(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	args := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	return args, nil
}
