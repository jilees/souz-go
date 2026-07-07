package graph

import "context"

// Node is a single executable step in an agent graph. Nodes are typed via
// NewNode but stored type-erased so the Runner can traverse heterogeneous graphs.
type Node struct {
	Name string
	exec func(ctx context.Context, in any) (any, error)
}

// NewNode creates a typed node from a strongly-typed function. I and O may be
// any types; the type assertion is done once at construction time so the runner
// itself is allocation-free per step.
func NewNode[I, O any](name string, fn func(ctx context.Context, in I) (O, error)) *Node {
	return &Node{
		Name: name,
		exec: func(ctx context.Context, in any) (any, error) {
			typed, ok := in.(I)
			if !ok {
				var zero I
				typed = zero
			}
			return fn(ctx, typed)
		},
	}
}

// Execute runs the node with the given input.
func (n *Node) Execute(ctx context.Context, in any) (any, error) {
	return n.exec(ctx, in)
}
