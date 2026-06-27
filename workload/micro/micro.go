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
// The read family runs on the synthetic generators so every reference is
// engine-independent: k-hop, varlen, and shortest path (directed and
// bidirectional) on the grid; directed and undirected triangle counts on ER; the
// point lookup, its negative variant, and the scan-and-aggregate over the grid's
// dense id column. Writes live in writes.go.
//
// See notes/Spec/2060/bench/05-workloads.md section 2 for the full catalog.
package micro

import (
	"fmt"
	"strconv"

	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
)

func init() {
	workload.Register(microGrid)
	workload.Register(microER)
}

// Pool key constants matching what workload.Curate writes to params.json.
const (
	khopKey      = "micro-khop"
	spKey        = "micro-sp"
	triangleKey  = "micro-triangle"
	pointKey     = "micro-point"
	pointMissKey = "micro-point-miss"
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
		spBidirQuery,
		pointQuery,
		pointMissQuery,
		scanQuery,
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
	ID:      "micro-khop1",
	Class:   target.Traversal,
	PoolKey: khopKey,
	Texts: map[workload.Dialect]string{
		workload.Cypher:     `MATCH (a:Node {id: $seed})-[:EDGE]->(b:Node) RETURN count(b) AS n`,
		workload.KuzuCypher: `MATCH (a:Node {id: CAST($seed AS INT64)})-[:EDGE]->(b:Node) RETURN count(b) AS n`,
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
	ID:      "micro-khop2",
	Class:   target.Traversal,
	PoolKey: khopKey,
	Texts: map[workload.Dialect]string{
		workload.Cypher:     `MATCH (a:Node {id: $seed})-[:EDGE]->()-[:EDGE]->(c:Node) RETURN count(DISTINCT c) AS n`,
		workload.KuzuCypher: `MATCH (a:Node {id: CAST($seed AS INT64)})-[:EDGE]->()-[:EDGE]->(c:Node) RETURN count(DISTINCT c) AS n`,
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
	ID:      "micro-khop3",
	Class:   target.Traversal,
	PoolKey: khopKey,
	Texts: map[workload.Dialect]string{
		workload.Cypher:     `MATCH (a:Node {id: $seed})-[:EDGE]->()-[:EDGE]->()-[:EDGE]->(d:Node) RETURN count(DISTINCT d) AS n`,
		workload.KuzuCypher: `MATCH (a:Node {id: CAST($seed AS INT64)})-[:EDGE]->()-[:EDGE]->()-[:EDGE]->(d:Node) RETURN count(DISTINCT d) AS n`,
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
	ID:      "micro-varlen",
	Class:   target.Traversal,
	PoolKey: khopKey,
	Texts: map[workload.Dialect]string{
		workload.Cypher:     `MATCH (a:Node {id: $seed})-[:EDGE*1..3]->(c:Node) RETURN count(DISTINCT c) AS n`,
		workload.KuzuCypher: `MATCH (a:Node {id: CAST($seed AS INT64)})-[:EDGE*1..3]->(c:Node) RETURN count(DISTINCT c) AS n`,
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
	ID:      "micro-sp",
	Class:   target.Traversal,
	PoolKey: spKey,
	Texts: map[workload.Dialect]string{
		// shortestPath() in openCypher (Neo4j dialect); gr and the Bolt engines accept it.
		workload.Cypher: `MATCH p = shortestPath((a:Node {id: $src})-[:EDGE*]->(b:Node {id: $dst})) RETURN length(p) AS d`,
		// Kuzu does not implement shortestPath(). It uses variable-length paths with
		// the SHORTEST keyword. The upper bound must be omitted (Kuzu caps bounded
		// variable-length rels at 30; unbounded 1.. removes that limit).
		workload.KuzuCypher: `MATCH (a:Node {id: CAST($src AS INT64)})-[r:EDGE* SHORTEST 1..]->(b:Node {id: CAST($dst AS INT64)}) RETURN length(r) AS d`,
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

// spBidirQuery is the bidirectional shortest path: the length of the shortest
// path between two nodes treating edges as undirected, the question a
// meet-in-the-middle search answers by growing two frontiers. It draws from the
// same (src, dst) pool as micro-sp; the reference is the undirected BFS distance,
// which equals what any correct bidirectional search returns. Class Subgraph
// because the two-frontier meet is a join-shaped pattern, not a single expand.
var spBidirQuery = &workload.WorkloadQuery{
	ID:      "micro-sp-bidir",
	Class:   target.Subgraph,
	PoolKey: spKey,
	Texts: map[workload.Dialect]string{
		workload.Cypher: `MATCH p = shortestPath((a:Node {id: $src})-[:EDGE*]-(b:Node {id: $dst})) RETURN length(p) AS d`,
		// Kuzu uses the SHORTEST keyword and an undirected pattern; the upper bound
		// is omitted so Kuzu does not cap the search at 30 hops.
		workload.KuzuCypher: `MATCH (a:Node {id: CAST($src AS INT64)})-[r:EDGE* SHORTEST 1..]-(b:Node {id: CAST($dst AS INT64)}) RETURN length(r) AS d`,
	},
	Reference: workload.RefStrategy{
		Compute: func(ds target.Dataset, p target.Params) (*target.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-sp-bidir reference: %w", err)
			}
			src, _ := p["src"].(string)
			dst, _ := p["dst"].(string)
			if src == "" || dst == "" {
				return nil, fmt.Errorf("micro-sp-bidir: params missing src or dst, got %v", p)
			}
			d, ok := g.ShortestPathUndirected(src, dst)
			if !ok {
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

// pointQuery is the indexed property probe: resolve one node by its id and return
// it. It is the cheapest read an engine does, the floor of the latency
// distribution. The reference is one row when the id exists; the pool holds only
// existing ids, so every draw returns a row.
var pointQuery = &workload.WorkloadQuery{
	ID:      "micro-point",
	Class:   target.PointRead,
	PoolKey: pointKey,
	Texts: map[workload.Dialect]string{
		workload.Cypher:     `MATCH (n:Node {id: $id}) RETURN n.id AS id`,
		workload.KuzuCypher: `MATCH (n:Node {id: CAST($id AS INT64)}) RETURN n.id AS id`,
	},
	Reference: workload.RefStrategy{
		Compute: func(ds target.Dataset, p target.Params) (*target.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-point reference: %w", err)
			}
			id, ok := p["id"].(string)
			if !ok {
				return nil, fmt.Errorf("micro-point: params missing string id, got %T", p["id"])
			}
			if !g.HasNode(id) {
				return &target.Answer{Columns: []string{"id"}, Rows: nil}, nil
			}
			n, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("micro-point: id %q is not an integer: %w", id, err)
			}
			return &target.Answer{
				Columns: []string{"id"},
				Rows:    [][]target.Value{{n}},
			}, nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// pointMissQuery is the negative lookup: probe an id that does not exist. It
// isolates the index miss path, which some engines short-circuit faster than a
// hit and some slower. The pool holds only absent ids, so the reference is always
// zero rows.
var pointMissQuery = &workload.WorkloadQuery{
	ID:      "micro-point-miss",
	Class:   target.PointRead,
	PoolKey: pointMissKey,
	Texts: map[workload.Dialect]string{
		workload.Cypher:     `MATCH (n:Node {id: $id}) RETURN n.id AS id`,
		workload.KuzuCypher: `MATCH (n:Node {id: CAST($id AS INT64)}) RETURN n.id AS id`,
	},
	Reference: workload.RefStrategy{
		Compute: func(ds target.Dataset, p target.Params) (*target.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-point-miss reference: %w", err)
			}
			id, ok := p["id"].(string)
			if !ok {
				return nil, fmt.Errorf("micro-point-miss: params missing string id, got %T", p["id"])
			}
			if g.HasNode(id) {
				return nil, fmt.Errorf("micro-point-miss: pool id %q exists; the miss pool is corrupt", id)
			}
			return &target.Answer{Columns: []string{"id"}, Rows: nil}, nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// scanQuery is the property scan and aggregate: read one column across the whole
// node population and aggregate it, with no traversal. It is the columnar-storage
// showcase, the "analytical-lite" contrast to the pointer-chasing reads. On the
// synthetic datasets it aggregates the dense numeric id column. It takes no
// parameters; the scan is over the whole graph.
var scanQuery = &workload.WorkloadQuery{
	ID:    "micro-scan",
	Class: target.Analytical,
	Texts: map[workload.Dialect]string{
		workload.Cypher: `MATCH (n:Node) RETURN count(n) AS n, avg(n.id) AS avgId`,
	},
	Params: workload.NewFixed(nil),
	Reference: workload.RefStrategy{
		Compute: func(ds target.Dataset, _ target.Params) (*target.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-scan reference: %w", err)
			}
			count, sum := g.ScanIDStats()
			var avg float64
			if count > 0 {
				avg = float64(sum) / float64(count)
			}
			return &target.Answer{
				Columns: []string{"n", "avgId"},
				Rows:    [][]target.Value{{count, avg}},
			}, nil
		},
		// avg is a float; coerce numbers and use the default float tolerance so an
		// engine that returns the count as a float still validates.
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
			// count(*) over the directed 3-cycle pattern has no ordering
			// constraint, so each distinct directed triangle is matched once per
			// rotation (3 times: a->b->c, b->c->a, c->a->b). Verified against gr.
			n := 3 * g.DirectedTriangles()
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
