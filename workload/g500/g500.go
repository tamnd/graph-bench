// Package g500 registers the Graph500 workload (spec 07 §5): the two
// timed kernels over an RMAT graph with the Graph500 initiator —
// g500-bfs (kernel 2, BFS) and g500-sssp (kernel 3, weighted SSSP) —
// each run from a pool of eight pre-drawn roots (the official spec draws
// 64; the full tier supplies them through a curated dataset pool under
// the same PoolKey). Fidelity is "derived": the harness RMAT generator
// approximates the official Graph500 Kronecker generator (same
// initiator, same scale/edge-factor parameterization, its own PRNG),
// and the graph is read directed where Graph500 reads it undirected.
//
// Validation: engines return levels at best through a query surface, so
// both kernels validate as distance arrays — BFS levels against
// workload.(*Graph).BFSLevels and SSSP distances against the Dijkstra
// oracle SSSPWeighted — rather than as parent trees. The spec's K2
// parent-tree checks (root its own parent, tree edges exist, levels
// differ by one) remain available as workload.(*Graph).ValidateBFSTree
// for adapters whose native kernel emits a parent array; such an adapter
// validates the tree per trial and reduces its output to levels for the
// answer comparison here.
//
// Metric: the headline is TEPS, not a percentile (spec 07 §5). The
// runner computes it per repetition as the oracle's EdgesReached(root)
// divided by the measured kernel time (measure.TEPS) and summarizes
// across roots with the harmonic mean (measure.HarmonicMeanTEPS),
// reported beside latency. Kernel 1 (construction) is the load metric,
// not a query.
package g500

import (
	"fmt"
	"strconv"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

func init() {
	workload.Register(g500Workload)
}

// roots is the pre-drawn eight-root pool (PoolKey "root") both kernels
// share, the Graph500 rule of timing every kernel from the same root
// set. Every token is under 1024 so the roots exist at rmat-10 as well
// as rmat-14: --profile fast runs the smoke variant, and a root the
// smoke graph does not carry fails the oracle rather than the engine.
var roots = []string{"0", "1", "97", "255", "511", "737", "900", "1023"}

var g500Workload = &workload.Workload{
	Name:            "g500",
	Title:           "Graph500 kernels: BFS (K2) and SSSP (K3) over RMAT, TEPS headline",
	Family:          "g500",
	Dataset:         "rmat-14-w",
	Fidelity:        "derived",
	Analytics:       true,
	ValidationScale: "s14-e16-w",
	Queries:         []*workload.Query{bfsKernel(), ssspKernel()},
}

// bfsKernel is Graph500 kernel 2: a full breadth-first traversal from a
// pooled root, validated as the level array.
func bfsKernel() *workload.Query {
	return &workload.Query{
		ID:        "g500-bfs",
		Class:     engine.Analytical,
		Algorithm: "bfs",
		PoolKey:   "root",
		Params:    rootPool(),
		Texts: map[engine.Dialect]string{
			// Memgraph's built-in *BFS expansion; the UNION ALL arm adds the
			// root's level-0 row the path pattern cannot produce. MAGE and
			// not Cypher: this is Memgraph's own path syntax and no other
			// Cypher engine parses it, so registered as Cypher it made
			// every one of them FAIL on a parse error where the honest
			// answer is that they have no spelling for this kernel.
			engine.MAGE: `MATCH p = (src:Node {id: $source})-[:EDGE *BFS]->(n:Node)
WHERE n <> src
RETURN n.id AS id, size(relationships(p)) AS level
UNION ALL
MATCH (src:Node {id: $source})
RETURN src.id AS id, 0 AS level`,
			// Kuzu has no BFS procedure, so the levels come out of a
			// shortest-path expansion, which is the same answer by a
			// longer road: the minimum hop count to every node the root
			// reaches. The root's own row is the UNION ALL arm, for the
			// same reason MAGE needs one.
			engine.KuzuCy: `MATCH (src:Node {id: CAST($source AS INT64)})-[r:EDGE* SHORTEST 1..]->(n:Node)
RETURN n.id AS id, length(r) AS level
UNION ALL
MATCH (src:Node {id: CAST($source AS INT64)})
RETURN src.id AS id, 0 AS level`,
			// The kernel spelling, for an engine that has one. Levels
			// follow stored edge direction and an unreached node comes
			// back null, which is the row the reference does not have.
			engine.ZuQL: `CALL bfs('EDGE', $source) YIELD node, level
WITH node, level WHERE level IS NOT NULL
RETURN node.id AS id, level ORDER BY id`,
		},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, params workload.Params) (*workload.Answer, error) {
				src, err := sourceOf(params)
				if err != nil {
					return nil, fmt.Errorf("g500-bfs: %w", err)
				}
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("g500-bfs reference: %w", err)
				}
				levels, ok := g.BFSLevels(src)
				if !ok {
					return nil, fmt.Errorf("g500-bfs: root %q not in graph", src)
				}
				rows := make([][]engine.Value, 0, len(levels))
				for _, l := range levels {
					id, err := idValue(l.ID)
					if err != nil {
						return nil, fmt.Errorf("g500-bfs: id %q not numeric: %w", l.ID, err)
					}
					rows = append(rows, []engine.Value{id, l.Val})
				}
				return &workload.Answer{Columns: []string{"id", "level"}, Rows: rows}, nil
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// ssspKernel is Graph500 kernel 3: weighted single-source shortest paths
// from the same roots over the uniform 1..255 "w" weights, validated as
// the distance array against the Dijkstra oracle.
func ssspKernel() *workload.Query {
	return &workload.Query{
		ID:        "g500-sssp",
		Class:     engine.Analytical,
		Algorithm: "sssp",
		PoolKey:   "root",
		Params:    rootPool(),
		Texts: map[engine.Dialect]string{
			// Memgraph's built-in weighted shortest path expansion.
			// Memgraph's own weighted shortest path syntax, under MAGE for
			// the same reason the kernel above is.
			engine.MAGE: `MATCH p = (src:Node {id: $source})-[:EDGE *WSHORTEST (r, n | r.w) total]->(n:Node)
WHERE n <> src
RETURN n.id AS id, total AS distance
UNION ALL
MATCH (src:Node {id: $source})
RETURN src.id AS id, 0 AS distance`,
			// zu answers this with a kernel too. sssp_weighted names
			// the weight column rather than assuming one, follows stored
			// edge direction, and reads a weight by the edge's slot in
			// the forward list, so a pair the generator emitted twice
			// keeps two weights and the cheaper copy is the one a path
			// takes. Unreached nodes come back null where the reference
			// has no row, the same filter bfs needs.
			engine.ZuQL: `CALL sssp_weighted('EDGE', $source, 'w') YIELD node, distance
WITH node, distance WHERE distance IS NOT NULL
RETURN node.id AS id, distance ORDER BY id`,
		},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, params workload.Params) (*workload.Answer, error) {
				src, err := sourceOf(params)
				if err != nil {
					return nil, fmt.Errorf("g500-sssp: %w", err)
				}
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("g500-sssp reference: %w", err)
				}
				dists, ok := g.SSSPWeighted(src)
				if !ok {
					return nil, fmt.Errorf("g500-sssp: root %q not in graph", src)
				}
				rows := make([][]engine.Value, 0, len(dists))
				for _, d := range dists {
					id, err := idValue(d.ID)
					if err != nil {
						return nil, fmt.Errorf("g500-sssp: id %q not numeric: %w", d.ID, err)
					}
					rows = append(rows, []engine.Value{id, d.Val})
				}
				return &workload.Answer{Columns: []string{"id", "distance"}, Rows: rows}, nil
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// rootPool builds the inline deterministic root pool: one draw per root
// token under the key "source", cycled in order. Both kernels take a
// fresh PoolSource so their draw cursors are independent but the
// sequences identical.
func rootPool() *workload.PoolSource {
	pool := make([]workload.Params, len(roots))
	for i, r := range roots {
		// IDValue, not the raw token: a curated pool binds a numeric
		// id as a number, and an engine with typed parameters is
		// entitled to the same value from either pool.
		pool[i] = workload.Params{"source": workload.IDValue(r)}
	}
	return workload.NewPoolSource(pool)
}

// sourceOf reads the "source" parameter as a node id token.
func sourceOf(params workload.Params) (string, error) {
	v, ok := params["source"]
	if !ok {
		return "", fmt.Errorf("missing source parameter")
	}
	switch s := v.(type) {
	case string:
		return s, nil
	case int64:
		return strconv.FormatInt(s, 10), nil
	}
	return "", fmt.Errorf("source parameter has type %T, want string or int64", v)
}

// idValue parses a dense numeric node id token to an int64.
func idValue(id string) (int64, error) {
	return strconv.ParseInt(id, 10, 64)
}
