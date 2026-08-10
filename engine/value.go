package engine

// Value is one of: nil, bool, int64, float64, string, time.Time, []Value,
// map[string]Value, Node, Rel, Path. Adapters decode engine-native values
// into this model at the Result boundary; numeric widths normalize to
// int64/float64.
type Value = any

// Node is a graph node in the canonical value model. ID is the engine's
// element id, diagnostic only: it is excluded from answer comparison
// unless a query's compare spec opts in.
type Node struct {
	ID     string
	Labels []string
	Props  map[string]Value
}

// Rel is a relationship in the canonical value model.
type Rel struct {
	ID    string
	Type  string
	Start string
	End   string
	Props map[string]Value
}

// Path is an alternating node/relationship sequence with
// len(Nodes) == len(Rels)+1.
type Path struct {
	Nodes []Node
	Rels  []Rel
}
