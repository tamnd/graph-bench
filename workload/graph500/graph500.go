// Package graph500 registers the Graph500 traversal kernel: one breadth-first
// search over an RMAT (Kronecker) graph, the standard Graph500 input, reporting
// the traversed edges per second (TEPS) beside the latency. It calls
// workload.Register in its init so any binary that imports it gets the "graph500"
// workload in the registry.
//
// The kernel is a single query, class Traversal: a full BFS from a fixed source,
// the same machinery as micro-khop run to exhaustion rather than to a fixed depth.
// Its reference is the BFS level array, computed by the breadth-first oracle and
// validated exactly, so an engine that traverses fast but wrong is caught. The
// RMAT generator is already in the suite (doc 04), so this family needs no new
// data; what it adds is the TEPS metric, the traversed-edge count over the
// traversal time (measure.TEPS), computed from the same reachable subgraph the
// reference walks. ExpectedEdges exposes that edge count so the measurement layer
// can divide it by the measured BFS duration.
//
// See notes/Spec/2060/bench/05-workloads.md section 8.3.
package graph500

import (
	"fmt"
	"strconv"

	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
)

// idValue parses a dense numeric node id token to an int64, the form an engine
// returns n.id; the RMAT generator emits an integer id space.
func idValue(id string) (int64, error) {
	return strconv.ParseInt(id, 10, 64)
}

func init() {
	workload.Register(graph500Workload)
}

// source is the fixed BFS root. Node 0 exists in every synthetic dataset, so the
// kernel is self-contained; Graph500 itself samples 64 roots, which the harness
// can drive by cycling the source over a curated pool in a larger run.
const source = "0"

var graph500Workload = &workload.Workload{
	Name:    "graph500",
	Title:   "Graph500 BFS traversal kernel over an RMAT graph (TEPS)",
	Dataset: "rmat",
	Queries: []*workload.WorkloadQuery{bfsKernel()},
}

// bfsKernel is the full breadth-first traversal from the source, returning each
// reachable node's level. The reference is the BFS level array; the TEPS metric
// divides ExpectedEdges by the measured traversal time.
func bfsKernel() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "g500-bfs",
		Class: target.Traversal,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (root:Node {id: $source})
MATCH p = shortestPath((root)-[:EDGE*]->(n:Node))
RETURN n.id AS id, length(p) AS level ORDER BY id`,
			// Kuzu reaches the level through a variable-length path with the SHORTEST
			// keyword; the upper bound is omitted so the search is not capped at 30.
			workload.KuzuCypher: `MATCH (root:Node {id: CAST($source AS INT64)})
MATCH (root)-[r:EDGE* SHORTEST 1..]->(n:Node)
RETURN n.id AS id, length(r) AS level ORDER BY id`,
		},
		Params: workload.NewFixed(target.Params{"source": source}),
		Reference: workload.RefStrategy{
			Compute: func(ds target.Dataset, _ target.Params) (*target.Answer, error) {
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("g500-bfs reference: %w", err)
				}
				levels, ok := g.BFSLevels(source)
				if !ok {
					return nil, fmt.Errorf("g500-bfs: source %q not in graph", source)
				}
				// The query starts at level 1 (it returns nodes reached over at least
				// one edge via shortestPath), so the root's level-0 row is dropped.
				rows := make([][]target.Value, 0, len(levels))
				for _, l := range levels {
					if l.Val == 0 {
						continue
					}
					id, err := idValue(l.ID)
					if err != nil {
						return nil, fmt.Errorf("g500-bfs: id %q not numeric: %w", l.ID, err)
					}
					rows = append(rows, []target.Value{id, l.Val})
				}
				return &target.Answer{Columns: []string{"id", "level"}, Rows: rows}, nil
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// ExpectedEdges returns the number of edges a BFS from the kernel's source
// traverses on the dataset, the numerator of the TEPS metric (measure.TEPS). The
// measurement layer divides it by the measured BFS duration to report TEPS beside
// the latency.
func ExpectedEdges(ds target.Dataset) (int64, error) {
	g, err := workload.LoadGraph(ds)
	if err != nil {
		return 0, fmt.Errorf("graph500 ExpectedEdges: %w", err)
	}
	edges, ok := g.EdgesReached(source)
	if !ok {
		return 0, fmt.Errorf("graph500 ExpectedEdges: source %q not in graph", source)
	}
	return edges, nil
}
