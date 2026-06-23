package target

// This file holds the canonical graph objects of the value model. An adapter
// maps its engine's nodes, relationships, and paths into these so validation
// compares two answers in one model. Element ids are engine-specific and are
// excluded from validation by default, because two engines assign different ids
// to the same logical node; they are carried for diagnostics only.

// Node is a node in the canonical value model: a label set, a property map, and
// an opaque element id. Validation compares labels and properties, not the id.
type Node struct {
	ID     string           // opaque, engine-specific element id (diagnostics only)
	Labels []string         // the node's labels
	Props  map[string]Value // the node's properties, in the value model
}

// Relationship is a relationship in the canonical value model: a single type,
// the start and end element ids, and a property map.
type Relationship struct {
	ID      string           // opaque, engine-specific element id (diagnostics only)
	Type    string           // the relationship's single type
	StartID string           // start node element id, preserving direction
	EndID   string           // end node element id
	Props   map[string]Value // the relationship's properties, in the value model
}

// Path is a path in the canonical value model: alternating nodes and
// relationships in traversal order, with len(Nodes) == len(Rels)+1.
type Path struct {
	Nodes []Node
	Rels  []Relationship
}
