package graph

import (
	"context"
	"errors"
	"fmt"
)

// RetryPolicy controls how transient node errors are retried.
type RetryPolicy struct {
	// MaxAttempts is the total number of execution attempts (first try + retries).
	// 0 or 1 means no retries.
	MaxAttempts int
	// ShouldRetry returns true if the error on the given attempt warrants another try.
	// attempt starts at 1.
	ShouldRetry func(err error, node *Node, attempt int) bool
}

// StepInfo is passed to the optional OnStep callback.
type StepInfo struct {
	Index int // 1-based step counter across the entire run
	Depth int // queue depth at the time this node was dequeued
}

// Runner executes a graph definition by iteratively draining a work queue.
// It is stateless and safe for concurrent use.
type Runner struct{}

// ErrMaxSteps is returned when execution exceeds the configured step limit.
var ErrMaxSteps = errors.New("graph: maximum steps exceeded")

type frame struct {
	node  *Node
	input any
	depth int
}

// Run executes the graph starting from start with the given seed input.
//
// It returns the output of the last-executed node, or an error. Execution stops
// when:
//   - the work queue is empty (normal termination)
//   - ctx is cancelled
//   - a node returns an error that is not retried
//   - stepLimit is exceeded
//
// stepLimit ≤ 0 defaults to 64. onStep may be nil.
func (r *Runner) Run(
	ctx context.Context,
	start *Node,
	seed any,
	def *Definition,
	stepLimit int,
	policy RetryPolicy,
	onStep func(info StepInfo, node *Node, in, out any),
) (any, error) {
	if stepLimit <= 0 {
		stepLimit = 64
	}
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = 1
	}

	queue := []frame{{node: start, input: seed, depth: 0}}
	var lastOut any
	step := 0

	for len(queue) > 0 {
		if ctx.Err() != nil {
			return lastOut, ctx.Err()
		}
		if step >= stepLimit {
			return lastOut, fmt.Errorf("%w (limit=%d)", ErrMaxSteps, stepLimit)
		}

		f := queue[0]
		queue = queue[1:]

		out, err := r.executeWithRetry(ctx, f.node, f.input, policy)
		if err != nil {
			return lastOut, fmt.Errorf("graph node %q: %w", f.node.Name, err)
		}
		lastOut = out
		step++

		if onStep != nil {
			onStep(StepInfo{Index: step, Depth: f.depth}, f.node, f.input, out)
		}

		for _, next := range def.NextNodes(f.node, out) {
			queue = append(queue, frame{node: next, input: out, depth: f.depth + 1})
		}
	}

	return lastOut, nil
}

func (r *Runner) executeWithRetry(ctx context.Context, node *Node, in any, policy RetryPolicy) (any, error) {
	var lastErr error
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		out, err := node.Execute(ctx, in)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if policy.ShouldRetry == nil || !policy.ShouldRetry(err, node, attempt) {
			return nil, err
		}
	}
	return nil, lastErr
}
