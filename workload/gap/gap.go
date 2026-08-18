// Package gap registers the GAP Benchmark Suite view of the whole-graph
// kernels (spec 07 §4): the four kernels shared with Graphalytics (BFS,
// SSSP, PageRank, connected components) plus the two GAP additions,
// triangle count and sampled betweenness centrality. Fidelity is
// "derived": the kernels and rules are GAP's (trials from pre-selected
// sources, uniform 1..255 weights generated with the graph, epsilon
// validation for PageRank), but the graph is the harness's own
// uniform-random generator at a reduced scale ("urand-14",
// 2^14 nodes, edge factor 16) rather than the official -u20/-g20 pair.
//
// Every query is Analytical and names its kernel in Algorithm; the
// runner SKIPs engines that do not declare the capability. Engines that
// reach kernels through a query surface run the engine.MAGE texts
// (Memgraph MAGE procedures and Memgraph's *BFS / *WSHORTEST
// expansions). Triangle count and betweenness carry no MAGE text: MAGE
// has no matching kernel surface (its betweenness is full and
// normalized, not the GAP source-sampled accumulation), so a MAGE cell
// there comes only from a declared native capability. Both do carry a
// zuQL text, since zu has a kernel for each under CALL.
//
// GAP sources are pre-selected: the four-entry pools below are the T=4
// default trials (full tier 64 comes from a curated dataset pool under
// the same PoolKey). Betweenness accumulates unnormalized pair
// dependencies over its fixed source sample, exactly what the oracle's
// Brandes implementation computes; it is the expensive oracle, so its
// SampleSize caps validation draws.
package gap

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
	"github.com/tamnd/graph-bench/workload/galytics"
)

func init() {
	workload.Register(gapWorkload)
}

const (
	// pageRankDamping and the 1e-4 comparison epsilon are GAP's PageRank
	// rules; the reference converges to 1e-12 so the epsilon budget
	// belongs to the engine, not the oracle.
	pageRankDamping = 0.85
	pageRankTol     = 1e-12
	pageRankMaxIter = 100
)

// trialSources is the T=4 pre-selected source pool for gap-bfs and
// gap-sssp: dense node-id tokens that exist in every urand graph this
// workload binds to.
//
// Every token is under 1024, which is the id space of urand-10, the
// smoke variant this runs at under --profile fast. Two of these used to
// be picked for the base scale alone and named no node down there, so
// the run died in the engine with "source names no node" halfway
// through the measurement. Nothing caught it because verification draws
// one source and both engines with a bfs kernel arrived after the pool
// was written.
var trialSources = []string{"0", "273", "511", "1000"}

// bcSources is the fixed betweenness source sample, the numeric ids
// BetweennessExact accumulates over.
var bcSources = []engine.Value{int64(0), int64(511), int64(8191), int64(12800)}

// bcSourceList renders bcSources as a list literal for a query text.
// The sample lives in one place and both the oracle and the text read
// it from there, so neither can be changed without the other following.
func bcSourceList() string {
	parts := make([]string, 0, len(bcSources))
	for _, v := range bcSources {
		parts = append(parts, fmt.Sprintf("%v", v))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

var gapWorkload = &workload.Workload{
	Name:            "gap",
	Title:           "GAP Benchmark Suite kernels (BFS, SSSP, PR, CC, TC, BC)",
	Family:          "gap",
	Dataset:         "urand-14",
	Fidelity:        "derived",
	Analytics:       true,
	ValidationScale: "s14-e16",
	Queries: []*workload.Query{
		bfsQuery(), ssspQuery(), pageRankQuery(), ccQuery(), tcQuery(), bcQuery(),
	},
}

// bfsQuery is breadth-first search from a pooled source, returning each
// reachable node's level.
func bfsQuery() *workload.Query {
	return &workload.Query{
		ID:        "gap-bfs",
		Class:     engine.Analytical,
		Algorithm: "bfs",
		PoolKey:   "bfs-source",
		Params:    sourcePool(trialSources),
		Texts: map[engine.Dialect]string{
			engine.MAGE: `MATCH p = (src:Node {id: $source})-[:EDGE *BFS]->(n:Node)
WHERE n <> src
RETURN n.id AS id, size(relationships(p)) AS level
UNION ALL
MATCH (src:Node {id: $source})
RETURN src.id AS id, 0 AS level`,
			// Kuzu has no BFS procedure, so the levels come out of a
			// shortest-path expansion, the same road g500-bfs takes there:
			// the minimum hop count to every node the source reaches. The
			// two kernels are the same question over different graphs, and
			// only g500-bfs had this text, so the whole GAP traversal
			// column read no-dialect-text for an engine that can in fact
			// answer it.
			engine.KuzuCy: `MATCH (src:Node {id: CAST($source AS INT64)})-[r:EDGE* SHORTEST 1..]->(n:Node)
RETURN n.id AS id, length(r) AS level
UNION ALL
MATCH (src:Node {id: CAST($source AS INT64)})
RETURN src.id AS id, 0 AS level`,
			// Neo4j community has no BFS without GDS, so the levels come
			// out of its own shortest path search, one per node the source
			// reaches: the minimum hop count to a node is its level. Both
			// of Neo4j's spellings were tried and this is the one that
			// answers. The GQL-flavored `ANY SHORTEST` quantified pattern
			// with the far end unbound ran past twenty minutes on the
			// thousand-node smoke graph before it was killed, where
			// shortestPath() over the same graph is seconds, so the
			// function is what is measured and the pattern is not a road
			// Neo4j can be held to. Cypher25 and not Cypher, since this is
			// the baseline text and no other engine here should resolve
			// it. The UNION ALL arm carries the source's own level-0 row,
			// which a path pattern cannot produce.
			engine.Cypher25: `MATCH (src:Node {id: $source}), (n:Node)
WHERE n <> src
MATCH p = shortestPath((src)-[:EDGE*]->(n))
RETURN n.id AS id, length(p) AS level
UNION ALL
MATCH (src:Node {id: $source})
RETURN src.id AS id, 0 AS level`,
			// zu answers this with a kernel rather than a pattern. bfs
			// follows stored edge direction, which is what a level means
			// here, and a node the source does not reach comes back null
			// where the reference has no row at all, so the filter is the
			// same filter.
			engine.ZuQL: `CALL bfs('EDGE', $source) YIELD node, level
WITH node, level WHERE level IS NOT NULL
RETURN node.id AS id, level ORDER BY id`,
		},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, params workload.Params) (*workload.Answer, error) {
				src, err := sourceOf(params)
				if err != nil {
					return nil, fmt.Errorf("gap-bfs: %w", err)
				}
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("gap-bfs reference: %w", err)
				}
				levels, ok := g.BFSLevels(src)
				if !ok {
					return nil, fmt.Errorf("gap-bfs: source %q not in graph", src)
				}
				rows := make([][]engine.Value, 0, len(levels))
				for _, l := range levels {
					id, err := idValue(l.ID)
					if err != nil {
						return nil, fmt.Errorf("gap-bfs: id %q not numeric: %w", l.ID, err)
					}
					rows = append(rows, []engine.Value{id, l.Val})
				}
				return &workload.Answer{Columns: []string{"id", "level"}, Rows: rows}, nil
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// ssspQuery is weighted single-source shortest paths from the same
// pooled sources, over the uniform 1..255 "w" weights urand always
// carries (the GAP SSSP rule).
func ssspQuery() *workload.Query {
	return &workload.Query{
		ID:        "gap-sssp",
		Class:     engine.Analytical,
		Algorithm: "sssp",
		PoolKey:   "sssp-source",
		Params:    sourcePool(trialSources),
		Texts: map[engine.Dialect]string{
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
					return nil, fmt.Errorf("gap-sssp: %w", err)
				}
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("gap-sssp reference: %w", err)
				}
				dists, ok := g.SSSPWeighted(src)
				if !ok {
					return nil, fmt.Errorf("gap-sssp: source %q not in graph", src)
				}
				return floatAnswer("gap-sssp", dists, "distance")
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// pageRankQuery scores every node by PageRank, validated at GAP's 1e-4
// epsilon.
func pageRankQuery() *workload.Query {
	return &workload.Query{
		ID:        "gap-pr",
		Class:     engine.Analytical,
		Algorithm: "pagerank",
		Params:    workload.Fixed{},
		Texts: map[engine.Dialect]string{
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
			// zu redistributes the dangling mass, so its ranks already
			// sum to one and there is nothing to normalize. It stops on
			// the same criterion this oracle stops on, the largest a
			// rank moves in a round falling under 1e-12, capped at 100
			// rounds. It used to stop at a fixed 20 and that is the one
			// thing that kept it out of this table.
			engine.ZuQL: `CALL pagerank('EDGE') YIELD node, rank
RETURN node.id AS id, rank AS score ORDER BY id`,
		},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, _ workload.Params) (*workload.Answer, error) {
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("gap-pr reference: %w", err)
				}
				return floatAnswer("gap-pr", g.PageRank(pageRankDamping, pageRankTol, pageRankMaxIter), "score")
			},
			Compare: workload.CompareSpec{FloatTol: 1e-4, CoerceNum: true},
		},
	}
}

// ccQuery labels every node with its weakly connected component in the
// canonical smallest-member labeling (galytics.NormalizeComponents is
// the exported normalization for adapters that surface raw ids; the
// text normalizes in-query).
func ccQuery() *workload.Query {
	return &workload.Query{
		ID:        "gap-cc",
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
			// zu names a component by the smallest id in it, which is the
			// canonical labeling this query wants, so there is nothing to
			// relabel.
			engine.ZuQL: `CALL wcc('EDGE') YIELD node, component
RETURN node.id AS id, component ORDER BY id`,
		},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, _ workload.Params) (*workload.Answer, error) {
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("gap-cc reference: %w", err)
				}
				comps := g.WeaklyConnectedComponents()
				rows := make([][]engine.Value, 0, len(comps))
				for _, c := range comps {
					id, err := idValue(c.ID)
					if err != nil {
						return nil, fmt.Errorf("gap-cc: id %q not numeric: %w", c.ID, err)
					}
					rows = append(rows, []engine.Value{id, c.Label})
				}
				ans := &workload.Answer{Columns: []string{"id", "component"}, Rows: rows}
				// The oracle already emits canonical labels; normalizing here
				// keeps the reference in the same canonical form the engine
				// side is reduced to, by the one shared routine.
				if err := galytics.NormalizeComponents(ans); err != nil {
					return nil, fmt.Errorf("gap-cc: %w", err)
				}
				return ans, nil
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// tcQuery is the global undirected triangle count, a single-row answer.
func tcQuery() *workload.Query {
	return &workload.Query{
		ID:        "gap-tc",
		Class:     engine.Analytical,
		Algorithm: "tc",
		Texts: map[engine.Dialect]string{
			// zu counts triangles with a kernel, which is what the GAP
			// TC benchmark measures. It yields the corners a node sits
			// on rather than one global number, so the sum comes back
			// divided by the three corners every triangle is counted
			// at. Direction is dropped and a pair joined twice is one
			// adjacency, the same graph the reference walks.
			//
			// micro-triangle-undirected asks the same question of the
			// join engine on purpose and keeps its pattern, so the two
			// stay a comparison of different things.
			engine.ZuQL: `CALL triangle_count('EDGE') YIELD node, triangles
WITH sum(triangles) AS corners
RETURN corners / 3 AS triangles`,
		},
		Params: workload.Fixed{},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, _ workload.Params) (*workload.Answer, error) {
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("gap-tc reference: %w", err)
				}
				return &workload.Answer{
					Columns: []string{"triangles"},
					Rows:    [][]engine.Value{{g.TriangleCountTotal()}},
				}, nil
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// bcQuery is betweenness centrality accumulated exactly (Brandes) over
// the fixed four-source sample, the GAP sampled-BC rule. The scores are
// unnormalized pair-dependency sums. Brandes over the whole pool is the
// expensive oracle, so SampleSize caps validation at one draw.
func bcQuery() *workload.Query {
	return &workload.Query{
		ID:        "gap-bc",
		Class:     engine.Analytical,
		Algorithm: "bc",
		Texts: map[engine.Dialect]string{
			// zu answers this with a kernel. The sources are inline
			// rather than bound because the adapter has no binding for a
			// list, and they are formatted from bcSources so the text
			// and the sample the oracle accumulates over cannot drift
			// apart.
			engine.ZuQL: fmt.Sprintf(`CALL betweenness('EDGE', %s) YIELD node, centrality
RETURN node.id AS id, centrality ORDER BY id`, bcSourceList()),
		},
		Params: workload.Fixed{P: workload.Params{"sources": bcSources}},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, params workload.Params) (*workload.Answer, error) {
				srcs, err := sourcesOf(params)
				if err != nil {
					return nil, fmt.Errorf("gap-bc: %w", err)
				}
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("gap-bc reference: %w", err)
				}
				return floatAnswer("gap-bc", g.BetweennessExact(srcs), "centrality")
			},
			Compare:    workload.CompareSpec{FloatTol: 1e-6, CoerceNum: true},
			SampleSize: 1,
		},
	}
}

// sourcePool builds the inline deterministic parameter pool: one draw
// per source token under the key "source".
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

// sourcesOf reads the "sources" parameter as the numeric id list the
// betweenness oracle takes.
func sourcesOf(params workload.Params) ([]int, error) {
	v, ok := params["sources"]
	if !ok {
		return nil, fmt.Errorf("missing sources parameter")
	}
	list, ok := v.([]engine.Value)
	if !ok {
		return nil, fmt.Errorf("sources parameter has type %T, want list", v)
	}
	out := make([]int, 0, len(list))
	for i, e := range list {
		switch n := e.(type) {
		case int64:
			out = append(out, int(n))
		case int:
			out = append(out, n)
		case string:
			p, err := strconv.Atoi(n)
			if err != nil {
				return nil, fmt.Errorf("sources[%d] %q is not numeric: %w", i, n, err)
			}
			out = append(out, p)
		default:
			return nil, fmt.Errorf("sources[%d] has type %T, want int64 or string", i, e)
		}
	}
	return out, nil
}

// idValue parses a dense numeric node id token to an int64.
func idValue(id string) (int64, error) {
	return strconv.ParseInt(id, 10, 64)
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
