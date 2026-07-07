package nodes

import (
	"context"
	"encoding/json"
	"fmt"

	"souz.ru/souz-go/pkg/agent"
	"souz.ru/souz-go/pkg/graph"
	"souz.ru/souz-go/pkg/providers"
	"souz.ru/souz-go/pkg/tools"
)

// NewToolLoop builds the "toolloop" graph node: it executes every tool call
// requested by the most recent assistant message against registry, and
// appends one RoleTool result message per call, keyed by ToolCallID so the
// provider can match results back to requests. Looping back to the llm node
// to let the model see the results is expressed at the graph-topology level
// (see BuildGraph), not here.
func NewToolLoop(registry map[string]tools.Tool) *graph.Node {
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
				Content:    executeTool(ctx, registry, call, in.InvocationMeta, sink),
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

func executeTool(ctx context.Context, registry map[string]tools.Tool, call providers.ToolCall, meta agent.InvocationMeta, sink agent.EventSink) string {
	sink.EmitToolCall(call.Name, string(call.Args))

	tool, ok := registry[call.Name]
	if !ok {
		msg := fmt.Sprintf("no such tool: %s", call.Name)
		sink.EmitToolResult(call.Name, msg, true)
		return msg
	}

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
