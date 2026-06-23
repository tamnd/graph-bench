// Package micro registers the micro-benchmark workload family. It calls
// workload.Register in its init so any binary that imports this package (blank
// or otherwise) gets the "micro-grid" and "micro-er" workloads in the registry.
// The harness imports it; tests import it for the workload catalog.
//
// The micro benchmarks are the v1 layer that isolates one engine behavior per
// query: one expand depth, one count, one scan, one write shape. They run on
// the synthetic generators (grid for reachability and path queries, ER for
// triangle counting) so the reference answer is either closed-form or computed
// by the oracle's simple routines rather than by a second engine.
//
// Writes and scan are deferred to the M3d and M7 slices; this package ships the
// read queries on synthetic datasets only. The SNB-family queries (micro-point,
// micro-scan) land in M7 when the SNB dataset is available.
//
// See notes/Spec/2060/bench/05-workloads.md section 2 for the full catalog.
package micro

import (
	"fmt"

	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
)

func init() {
	workload.Register(microGrid)
	workload.Register(microER)
}

// Pool key constants matching what workload.Curate writes to params.json.
const (
	khopKey     = "micro-khop"
	spKey       = "micro-sp"
	triangleKey = "micro-triangle"
)

// microGrid is the reachability and path family on a 4-neighbor grid, where
// every distance is the Manhattan distance and every count is closed-form. The
// queries here validate the oracle and exercise the engine's expand and path
// operators on the cleanest possible dataset.
var microGrid = &workload.Workload{
	Name:    "micro-grid",
	Title:   "Micro-benchmarks on a grid dataset (k-hop, varlen, shortest path)",
	Dataset: "grid",
	Queries: []*workload.WorkloadQuery{
		khop1Query,
		khop2Query,
		khop3Query,
		varlenQuery,
		spQuery,
	},
}

// microER is the triangle family on an Erdős–Rényi graph, where the triangle
// count has a clean closed-form expectation for validation.
var microER = &workload.Workload{
	Name:    "micro-er",
	Title:   "Micro-benchmarks on an ER dataset (directed and undirected triangle counts)",
	Dataset: "er",
	Queries: []*workload.WorkloadQuery{
		triangleDirectedQuery,
		triangleUndirectedQuery,
	},
}

// khop1Query is the one-hop neighborhood expansion: follow one edge from the
// seed, count the neighbors. It isolates the single-expand operation and is the
// atom of traversal.
var khop1Query = &workload.WorkloadQuery{
	ID:    "micro-khop1",
	Class: target.Traversal,
	Texts: map[workload.Dialect]string{
		workload.Cypher: `MATCH (a:Node {id: $seed})-[:EDGE]->(b:Node) RETURN count(b) AS n`,
	},
	Reference: workload.RefStrategy{
		Compute: func(ds target.Dataset, p target.Params) (*target.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-khop1 reference: %w", err)
			}
			seed, ok := p["seed"].(string)
			if !ok {
				return nil, fmt.Errorf("micro-khop1: params missing string seed, got %T", p["seed"])
			}
			n := int64(g.OutDegree(seed))
			return &target.Answer{
				Columns: []string{"n"},
				Rows:    [][]target.Value{{n}},
			}, nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// khop2Query is the two-hop expansion: distinct nodes reachable in exactly two
// hops from the seed. It exercises the engine's double-expand and distinct
// deduplication.
var khop2Query = &workload.WorkloadQuery{
	ID:    "micro-khop2",
	Class: target.Traversal,
	Texts: map[workload.Dialect]string{
		workload.Cypher: `MATCH (a:Node {id: $seed})-[:EDGE]->()-[:EDGE]->(c:Node) RETURN count(DISTINCT c) AS n`,
	},
	Reference: workload.RefStrategy{
		Compute: func(ds target.Dataset, p target.Params) (*target.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-khop2 reference: %w", err)
			}
			seed, ok := p["seed"].(string)
			if !ok {
				return nil, fmt.Errorf("micro-khop2: params missing string seed, got %T", p["seed"])
			}
			n := int64(g.ReachableExact(seed, 2))
			return &target.Answer{
				Columns: []string{"n"},
				Rows:    [][]target.Value{{n}},
			}, nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// khop3Query is the three-hop expansion, the third depth in the k-hop sweep.
var khop3Query = &workload.WorkloadQuery{
	ID:    "micro-khop3",
	Class: target.Traversal,
	Texts: map[workload.Dialect]string{
		workload.Cypher: `MATCH (a:Node {id: $seed})-[:EDGE]->()-[:EDGE]->()-[:EDGE]->(d:Node) RETURN count(DISTINCT d) AS n`,
	},
	Reference: workload.RefStrategy{
		Compute: func(ds target.Dataset, p target.Params) (*target.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-khop3 reference: %w", err)
			}
			seed, ok := p["seed"].(string)
			if !ok {
				return nil, fmt.Errorf("micro-khop3: params missing string seed, got %T", p["seed"])
			}
			n := int64(g.ReachableExact(seed, 3))
			return &target.Answer{
				Columns: []string{"n"},
				Rows:    [][]target.Value{{n}},
			}, nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// varlenQuery is the variable-length 1-to-3-hop expansion: the union of nodes
// reachable in one, two, or three hops. It exercises the engine's variable-length
// operator and the frontier union.
var varlenQuery = &workload.WorkloadQuery{
	ID:    "micro-varlen",
	Class: target.Traversal,
	Texts: map[workload.Dialect]string{
		workload.Cypher: `MATCH (a:Node {id: $seed})-[:EDGE*1..3]->(c:Node) RETURN count(DISTINCT c) AS n`,
	},
	Reference: workload.RefStrategy{
		Compute: func(ds target.Dataset, p target.Params) (*target.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-varlen reference: %w", err)
			}
			seed, ok := p["seed"].(string)
			if !ok {
				return nil, fmt.Errorf("micro-varlen: params missing string seed, got %T", p["seed"])
			}
			n := int64(g.ReachableRange(seed, 1, 3))
			return &target.Answer{
				Columns: []string{"n"},
				Rows:    [][]target.Value{{n}},
			}, nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// spQuery is the single-pair shortest path: the length of the shortest directed
// path from src to dst. A pair with no path has no row in the engine's answer.
var spQuery = &workload.WorkloadQuery{
	ID:    "micro-sp",
	Class: target.Traversal,
	Texts: map[workload.Dialect]string{
		// shortestPath in openCypher (undirected). For directed use -[:EDGE]->
		// consistently; gr and the Bolt engines accept both.
		workload.Cypher: `MATCH p = shortestPath((a:Node {id: $src})-[:EDGE*]->(b:Node {id: $dst})) RETURN length(p) AS d`,
	},
	Reference: workload.RefStrategy{
		Compute: func(ds target.Dataset, p target.Params) (*target.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-sp reference: %w", err)
			}
			src, _ := p["src"].(string)
			dst, _ := p["dst"].(string)
			if src == "" || dst == "" {
				return nil, fmt.Errorf("micro-sp: params missing src or dst, got %v", p)
			}
			d, ok := g.ShortestPath(src, dst)
			if !ok {
				// Unreachable pair: no row.
				return &target.Answer{Columns: []string{"d"}, Rows: nil}, nil
			}
			return &target.Answer{
				Columns: []string{"d"},
				Rows:    [][]target.Value{{int64(d)}},
			}, nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// triangleDirectedQuery counts directed 3-cycles a->b->c->a over the whole
// graph. It is the WCOJ showcase: a binary-join plan materializes all 2-paths
// while a worst-case-optimal join intersects adjacency lists.
var triangleDirectedQuery = &workload.WorkloadQuery{
	ID:    "micro-triangle",
	Class: target.Subgraph,
	Texts: map[workload.Dialect]string{
		workload.Cypher: `MATCH (a:Node)-[:EDGE]->(b:Node)-[:EDGE]->(c:Node)-[:EDGE]->(a) RETURN count(*) AS n`,
	},
	Reference: workload.RefStrategy{
		Compute: func(ds target.Dataset, _ target.Params) (*target.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-triangle reference: %w", err)
			}
			n := g.DirectedTriangles()
			return &target.Answer{
				Columns: []string{"n"},
				Rows:    [][]target.Value{{n}},
			}, nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// triangleUndirectedQuery counts undirected triangles (each unordered triple
// once) in the underlying undirected graph.
var triangleUndirectedQuery = &workload.WorkloadQuery{
	ID:    "micro-triangle-undirected",
	Class: target.Subgraph,
	Texts: map[workload.Dialect]string{
		workload.Cypher: `MATCH (a:Node)-[:EDGE]-(b:Node)-[:EDGE]-(c:Node)-[:EDGE]-(a) WHERE id(a) < id(b) AND id(b) < id(c) RETURN count(*) AS n`,
	},
	Reference: workload.RefStrategy{
		Compute: func(ds target.Dataset, _ target.Params) (*target.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-triangle-undirected reference: %w", err)
			}
			n := g.UndirectedTriangles()
			return &target.Answer{
				Columns: []string{"n"},
				Rows:    [][]target.Value{{n}},
			}, nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}
