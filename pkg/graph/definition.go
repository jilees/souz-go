package graph

// EdgeCondition returns true when the outgoing edge should be followed.
// A nil condition means unconditional (always follow).
type EdgeCondition func(out any) bool

type conditionalEdge struct {
	cond EdgeCondition
	to   *Node
}

// Definition holds the graph topology as a set of conditional edges.
type Definition struct {
	edges map[*Node][]conditionalEdge
}

// NewDefinition creates an empty graph definition.
func NewDefinition() *Definition {
	return &Definition{edges: make(map[*Node][]conditionalEdge)}
}

// AddEdge adds an unconditional edge from → to. Every execution of from will
// proceed to to regardless of the output value.
func (d *Definition) AddEdge(from, to *Node) {
	d.edges[from] = append(d.edges[from], conditionalEdge{to: to})
}

// AddConditionalEdge adds a guarded edge: to is only visited when cond(output) == true.
func (d *Definition) AddConditionalEdge(from *Node, cond EdgeCondition, to *Node) {
	d.edges[from] = append(d.edges[from], conditionalEdge{cond: cond, to: to})
}

// NextNodes returns all successor nodes after node produced out.
func (d *Definition) NextNodes(node *Node, out any) []*Node {
	var next []*Node
	for _, e := range d.edges[node] {
		if e.cond == nil || e.cond(out) {
			next = append(next, e.to)
		}
	}
	return next
}
