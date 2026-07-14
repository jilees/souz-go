package agent

import (
	"context"
	"fmt"

	"souz.ru/souz-go/pkg/graph"
	"souz.ru/souz-go/pkg/hooks"
	"souz.ru/souz-go/pkg/providers"
)

// TurnResult is the output of a completed agent turn.
type TurnResult struct {
	Output  string
	Context AgentContext
	Usage   providers.Usage
}

// Executor drives one agent turn through a pre-built graph. Assembling the
// graph itself (wiring concrete nodes together) happens outside this
// package — see pkg/agent/nodes.BuildGraph — because pkg/agent/nodes
// imports pkg/agent, so pkg/agent importing it back would be a cycle.
type Executor struct {
	runner    *graph.Runner
	def       *graph.Definition
	start     *graph.Node
	policy    graph.RetryPolicy
	stepLimit int
	turnHooks *hooks.Hooks
}

// NewExecutor builds an Executor that runs def starting at start.
// stepLimit <= 0 defaults to 64 (see graph.Runner.Run). turnHooks may be
// nil, which disables it — Execute runs every turn the same way either
// way, this is the single choke point every caller (channels, HTTP API,
// ...) goes through, so wiring turnHooks here covers all of them.
func NewExecutor(def *graph.Definition, start *graph.Node, policy graph.RetryPolicy, stepLimit int, turnHooks *hooks.Hooks) *Executor {
	return &Executor{
		runner:    &graph.Runner{},
		def:       def,
		start:     start,
		policy:    policy,
		stepLimit: stepLimit,
		turnHooks: turnHooks,
	}
}

// Execute runs the agent graph for a single user turn.
func (e *Executor) Execute(ctx context.Context, seed AgentContext) (*TurnResult, error) {
	if e.turnHooks != nil {
		end := e.turnHooks.StartTurn()
		defer end()
	}

	out, err := e.runner.Run(ctx, e.start, seed, e.def, e.stepLimit, e.policy, nil)
	if err != nil {
		return nil, fmt.Errorf("agent turn: %w", err)
	}

	ac, ok := out.(AgentContext)
	if !ok {
		ac = seed
	}
	return &TurnResult{
		Output:  lastAssistantText(ac.History),
		Context: ac,
	}, nil
}

func lastAssistantText(history []providers.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == providers.RoleAssistant && history[i].Content != "" {
			return history[i].Content
		}
	}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == providers.RoleAssistant {
			return history[i].Content
		}
	}
	return ""
}
