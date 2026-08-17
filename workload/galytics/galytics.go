// Package galytics registers the LDBC Graphalytics kernel workloads
// (spec 07 §4): the six whole-graph algorithms — BFS, PageRank, weakly
// connected components, community detection by label propagation, local
// clustering coefficient, and single-source shortest paths — each as one
// Analytical query whose Algorithm field names the native kernel an
// engine must declare to run it. The runner SKIPs engines without the
// capability; engines that reach kernels through a query surface run the
// engine.MAGE texts, written as Memgraph MAGE procedure calls
// (pagerank.get, weakly_connected_components.get, community_detection.get)
// and Memgraph's *BFS / *WSHORTEST path expansions. There is deliberately
// no engine.Cypher spelling: none of these kernels has one, and an engine
// without the procedures SKIPs with "no-dialect-text" rather than running
// a pure-Cypher emulation, which would measure the emulation (spec 07 §1
// rule 3).
//
// Two workloads are registered because the kernels bind to two datasets:
// "galytics" holds the five unweighted kernels on "rmat-14", and
// "galytics-w" holds ga-sssp on "rmat-14-w", the same graph with the
// uniform 1..255 edge weights the weighted SSSP oracle (Dijkstra over
// the "w" property) requires.
//
// Determinism follows the LDBC choices baked into the oracle
// (workload/analytics_oracle.go): BFS reports levels not parents, CDLP
// runs fixed synchronous rounds with smallest-label tie-break, and WCC
// labels a component by its smallest member id. The last is the
// label-invariance rule: an engine's arbitrary component ids reduce to
// the canonical labeling either in-query (the WCC text relabels by
// min(node.id)) or through NormalizeComponents, exported for adapters
// that surface raw component ids.
package galytics

import (
	"fmt"
	"math"
	"strconv"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

func init() {
	workload.Register(unweighted)
	workload.Register(weightedSSSP)
}

const (
	// pageRankDamping is the standard Graphalytics damping factor.
	pageRankDamping = 0.85
	// pageRankTol converges the reference well past the 1e-4 comparison
	// epsilon (the Graphalytics validation tolerance), so the oracle is
	// effectively exact and the epsilon budget belongs to the engine.
	pageRankTol     = 1e-12
	pageRankMaxIter = 100
	// cdlpRounds is the fixed synchronous round count of the CDLP kernel;
	// Graphalytics parameterizes the iteration count per dataset and the
	// harness fixes it at 10 for the rmat binding.
	cdlpRounds = 10
)

// bfsSources is the curated deterministic source pool for ga-bfs and
// ga-sssp (PoolKey "bfs-source" / "sssp-source"). A curated dataset
// pool under the same key overrides the inline pool.
//
// Every token is under 1024, which is the id space of rmat-10, the
// smoke variant this workload runs at under --profile fast. A pool
// picked for the base scale alone makes every focused run fail in the
// oracle with "source not in graph", which is a bug in the pool and
// reads like a bug in the engine.
var bfsSources = []string{"0", "273", "512", "1000"}

// unweighted is the "galytics" workload: the five unweighted kernels.
var unweighted = &workload.Workload{
	Name:            "galytics",
	Title:           "LDBC Graphalytics kernels (BFS, PageRank, WCC, CDLP, LCC)",
	Family:          "galytics",
	Dataset:         "rmat-14",
	Fidelity:        "spec-following",
	Analytics:       true,
	ValidationScale: "s14-e16",
	Queries: []*workload.Query{
		bfsQuery(), pageRankQuery(), wccQuery(), cdlpQuery(), lccQuery(),
	},
}

// weightedSSSP is the "galytics-w" workload: ga-sssp on the weighted
// twin of the same graph (identical edge set, w in 1..255).
var weightedSSSP = &workload.Workload{
	Name:            "galytics-w",
	Title:           "LDBC Graphalytics weighted kernel (SSSP)",
	Family:          "galytics",
	Dataset:         "rmat-14-w",
	Fidelity:        "spec-following",
	Analytics:       true,
	ValidationScale: "s14-e16-w",
	Queries:         []*workload.Query{ssspQuery()},
}

// bfsQuery is breadth-first search from a pooled source, returning each
// reachable node's level. Level, not parent, so two engines agree.
func bfsQuery() *workload.Query {
	return &workload.Query{
		ID:        "ga-bfs",
		Class:     engine.Analytical,
		Algorithm: "bfs",
		PoolKey:   "bfs-source",
		Params:    sourcePool(bfsSources),
		Texts: map[engine.Dialect]string{
			// Memgraph's built-in *BFS expansion; the UNION ALL arm adds the
			// root's level-0 row the path pattern cannot produce.
			engine.MAGE: `MATCH p = (src:Node {id: $source})-[:EDGE *BFS]->(n:Node)
WHERE n <> src
RETURN n.id AS id, size(relationships(p)) AS level
UNION ALL
MATCH (src:Node {id: $source})
RETURN src.id AS id, 0 AS level`,
			// Kùzu has no BFS procedure, so the levels come out of a
			// shortest-path expansion, which is the same answer by a
			// longer road: the minimum hop count to every node the
			// source reaches. The root's own row is the UNION ALL arm,
			// for the same reason MAGE needs one.
			engine.KuzuCy: `MATCH (src:Node {id: CAST($source AS INT64)})-[r:EDGE* SHORTEST 1..]->(n:Node)
RETURN n.id AS id, length(r) AS level
UNION ALL
MATCH (src:Node {id: CAST($source AS INT64)})
RETURN src.id AS id, 0 AS level`,
			// zu answers this one with a kernel rather than a pattern.
			// bfs follows stored edge direction, which is what a level
			// means here; unreached nodes come back null and the
			// reference omits them, so the WHERE is the same filter.
			engine.ZuQL: `CALL bfs('edge', $source) YIELD node, level
WITH node, level WHERE level IS NOT NULL
RETURN node.id AS id, level ORDER BY id`,
		},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, params workload.Params) (*workload.Answer, error) {
				src, err := sourceOf(params)
				if err != nil {
					return nil, fmt.Errorf("ga-bfs: %w", err)
				}
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("ga-bfs reference: %w", err)
				}
				levels, ok := g.BFSLevels(src)
				if !ok {
					return nil, fmt.Errorf("ga-bfs: source %q not in graph", src)
				}
				return intAnswer("ga-bfs", levels, "level")
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// pageRankQuery scores every node by PageRank; validation is
// epsilon-based at the Graphalytics tolerance 1e-4 (spec 07 §1).
func pageRankQuery() *workload.Query {
	return &workload.Query{
		ID:        "ga-pr",
		Class:     engine.Analytical,
		Algorithm: "pagerank",
		Params:    workload.Fixed{},
		Texts: map[engine.Dialect]string{
			// MAGE defaults: 100 iterations, damping 0.85.
			// Kùzu's kernel is reached through a named projection the adapter
			// creates at load ("gb"). Two adjustments make it answer the same
			// question as the reference rather than a nearby one, and both are
			// about the question, not about going faster.
			//
			// Its ranks are not sum-normalized: the mass on dangling nodes is
			// dropped instead of redistributed, so they sum to less than 1 and
			// the division below restores the LDBC scale. The convergence
			// criterion is stated for the same reason the MAGE text records
			// MAGE's — the default stops at 20 iterations, which is a
			// different, less-converged answer. Together they land within
			// 1.2e-10 per node of this oracle on rmat-10; normalization alone
			// left 3.4e-4 relative, outside the 1e-4 tolerance, and the gap
			// was convergence rather than the dropped mass.
			engine.KuzuCy: `CALL page_rank('gb', maxIterations := 100, tolerance := 0.000000001)
WITH collect({i: node.id, r: rank}) AS ranks, sum(rank) AS total
UNWIND ranks AS x
RETURN x.i AS id, x.r/total AS score ORDER BY id`,
			engine.MAGE: `CALL pagerank.get() YIELD node, rank
RETURN node.id AS id, rank AS score ORDER BY id`,
		},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, _ workload.Params) (*workload.Answer, error) {
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("ga-pr reference: %w", err)
				}
				return floatAnswer("ga-pr", g.PageRank(pageRankDamping, pageRankTol, pageRankMaxIter), "score")
			},
			Compare: workload.CompareSpec{FloatTol: 1e-4, CoerceNum: true},
		},
	}
}

// wccQuery labels every node with its weakly connected component, the
// component named by its smallest member id (the canonical labeling).
// The text performs the same normalization in-query so an engine's
// arbitrary component ids compare label-invariantly.
func wccQuery() *workload.Query {
	return &workload.Query{
		ID:        "ga-wcc",
		Class:     engine.Analytical,
		Algorithm: "wcc",
		Params:    workload.Fixed{},
		Texts: map[engine.Dialect]string{
			// Kùzu labels a component by an arbitrary group id, so the text
			// relabels by the smallest member id — the same canonical form
			// the oracle emits — before returning.
			engine.KuzuCy: `CALL weakly_connected_components('gb')
WITH group_id, min(node.id) AS label, collect(node.id) AS ids
UNWIND ids AS nid
RETURN nid AS id, label AS component ORDER BY id`,
			engine.MAGE: `CALL weakly_connected_components.get() YIELD node, component_id
WITH component_id, min(node.id) AS label, collect(node) AS members
UNWIND members AS m
RETURN m.id AS id, label AS component ORDER BY id`,
			// zu names a component by the smallest row in it, which is
			// the smallest id only when the loader kept ids and rows in
			// step. The relabel does not assume it did.
			engine.ZuQL: `CALL wcc('edge') YIELD node, component
WITH component, min(node.id) AS label, collect(node.id) AS ids
UNWIND ids AS nid
RETURN nid AS id, label AS component ORDER BY id`,
		},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, _ workload.Params) (*workload.Answer, error) {
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("ga-wcc reference: %w", err)
				}
				return labelAnswer("ga-wcc", g.WeaklyConnectedComponents(), "component")
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// cdlpQuery assigns every node a community by synchronous label
// propagation, smallest-label tie-break, a fixed round count.
func cdlpQuery() *workload.Query {
	return &workload.Query{
		ID:        "ga-cdlp",
		Class:     engine.Analytical,
		Algorithm: "cdlp",
		Params:    workload.Fixed{},
		Texts: map[engine.Dialect]string{
			// MAGE's community_detection (Louvain) is the closest procedure
			// surface; its communities are not the LDBC LPA partition, so an
			// engine running this text is measured but expected to mismatch
			// validation unless it exposes true label propagation.
			engine.MAGE: `CALL community_detection.get() YIELD node, community_id
RETURN node.id AS id, community_id AS community ORDER BY id`,
			// zu propagates the stored ids, not its own row numbers, so
			// the smallest-label tie-break decides the same way the
			// oracle does and no relabeling is possible afterwards.
			engine.ZuQL: `CALL cdlp('edge') YIELD node, community
RETURN node.id AS id, community ORDER BY id`,
		},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, _ workload.Params) (*workload.Answer, error) {
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("ga-cdlp reference: %w", err)
				}
				return labelAnswer("ga-cdlp", g.LabelPropagation(cdlpRounds), "community")
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// lccQuery computes the directed local clustering coefficient of every
// node. MAGE ships no LCC kernel, so the Cypher engines carry no text
// and run it only through a declared native "lcc" capability.
func lccQuery() *workload.Query {
	return &workload.Query{
		ID:        "ga-lcc",
		Class:     engine.Analytical,
		Algorithm: "lcc",
		Params:    workload.Fixed{},
		Texts: map[engine.Dialect]string{
			engine.ZuQL: `CALL lcc('edge') YIELD node, coefficient
RETURN node.id AS id, coefficient ORDER BY id`,
		},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, _ workload.Params) (*workload.Answer, error) {
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("ga-lcc reference: %w", err)
				}
				return floatAnswer("ga-lcc", g.LocalClustering(), "coefficient")
			},
			Compare: workload.CompareSpec{FloatTol: 1e-4, CoerceNum: true},
		},
	}
}

// ssspQuery computes weighted single-source shortest paths from a pooled
// source over the "w" edge property (Dijkstra oracle), on "rmat-14-w".
func ssspQuery() *workload.Query {
	return &workload.Query{
		ID:        "ga-sssp",
		Class:     engine.Analytical,
		Algorithm: "sssp",
		PoolKey:   "sssp-source",
		Params:    sourcePool(bfsSources),
		Texts: map[engine.Dialect]string{
			// Memgraph's built-in weighted shortest path expansion.
			engine.MAGE: `MATCH p = (src:Node {id: $source})-[:EDGE *WSHORTEST (r, n | r.w) total]->(n:Node)
WHERE n <> src
RETURN n.id AS id, total AS distance
UNION ALL
MATCH (src:Node {id: $source})
RETURN src.id AS id, 0 AS distance`,
		},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, params workload.Params) (*workload.Answer, error) {
				src, err := sourceOf(params)
				if err != nil {
					return nil, fmt.Errorf("ga-sssp: %w", err)
				}
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("ga-sssp reference: %w", err)
				}
				dists, ok := g.SSSPWeighted(src)
				if !ok {
					return nil, fmt.Errorf("ga-sssp: source %q not in graph", src)
				}
				return floatAnswer("ga-sssp", dists, "distance")
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// NormalizeComponents rewrites the label column of a two-column
// (id, label) answer in place so every component (group of rows sharing
// a label) is labeled by its smallest member id — the canonical form the
// WCC reference emits. It makes an engine's arbitrary component ids
// comparable: two labelings that induce the same partition normalize to
// the same answer, and two different partitions cannot. Both columns
// must be numeric.
func NormalizeComponents(ans *workload.Answer) error {
	if ans == nil {
		return fmt.Errorf("normalize components: nil answer")
	}
	if len(ans.Columns) != 2 {
		return fmt.Errorf("normalize components: want 2 columns (id, label), got %v", ans.Columns)
	}
	smallest := map[int64]int64{} // arbitrary label -> smallest member id
	for i, row := range ans.Rows {
		if len(row) != 2 {
			return fmt.Errorf("normalize components: row %d has %d columns, want 2", i, len(row))
		}
		id, ok := numValue(row[0])
		if !ok {
			return fmt.Errorf("normalize components: row %d id %v (%T) is not numeric", i, row[0], row[0])
		}
		label, ok := numValue(row[1])
		if !ok {
			return fmt.Errorf("normalize components: row %d label %v (%T) is not numeric", i, row[1], row[1])
		}
		if m, seen := smallest[label]; !seen || id < m {
			smallest[label] = id
		}
	}
	for _, row := range ans.Rows {
		label, _ := numValue(row[1])
		row[1] = smallest[label]
	}
	return nil
}

// numValue reads an integer-valued engine.Value (int64 canonically, plus
// the widths an adapter might pass through).
func numValue(v engine.Value) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case int32:
		return int64(x), true
	case float64:
		if x == math.Trunc(x) {
			return int64(x), true
		}
	}
	return 0, false
}

// sourcePool builds the inline deterministic parameter pool: one draw
// per source token under the key "source", cycled in order so every
// engine sees the identical sequence.
func sourcePool(sources []string) *workload.PoolSource {
	pool := make([]workload.Params, len(sources))
	for i, s := range sources {
		// IDValue, not the raw token: a curated pool binds a numeric
		// id as a number, and an engine with typed parameters is
		// entitled to the same value from either pool.
		pool[i] = workload.Params{"source": workload.IDValue(s)}
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

// idValue parses a dense numeric node id token to an int64, the form an
// engine returns n.id; the synthetic generators emit an integer id space.
func idValue(id string) (int64, error) {
	return strconv.ParseInt(id, 10, 64)
}

// intAnswer renders per-node integer results as an (id, col) answer.
func intAnswer(q string, vals []workload.NodeInt, col string) (*workload.Answer, error) {
	rows := make([][]engine.Value, 0, len(vals))
	for _, v := range vals {
		id, err := idValue(v.ID)
		if err != nil {
			return nil, fmt.Errorf("%s: id %q not numeric: %w", q, v.ID, err)
		}
		rows = append(rows, []engine.Value{id, v.Val})
	}
	return &workload.Answer{Columns: []string{"id", col}, Rows: rows}, nil
}

// floatAnswer renders per-node float results as an (id, col) answer.
func floatAnswer(q string, vals []workload.NodeFloat, col string) (*workload.Answer, error) {
	rows := make([][]engine.Value, 0, len(vals))
	for _, v := range vals {
		id, err := idValue(v.ID)
		if err != nil {
			return nil, fmt.Errorf("%s: id %q not numeric: %w", q, v.ID, err)
		}
		rows = append(rows, []engine.Value{id, v.Val})
	}
	return &workload.Answer{Columns: []string{"id", col}, Rows: rows}, nil
}

// labelAnswer renders per-node label results as an (id, col) answer.
func labelAnswer(q string, vals []workload.NodeLabel, col string) (*workload.Answer, error) {
	rows := make([][]engine.Value, 0, len(vals))
	for _, v := range vals {
		id, err := idValue(v.ID)
		if err != nil {
			return nil, fmt.Errorf("%s: id %q not numeric: %w", q, v.ID, err)
		}
		rows = append(rows, []engine.Value{id, v.Label})
	}
	return &workload.Answer{Columns: []string{"id", col}, Rows: rows}, nil
}
