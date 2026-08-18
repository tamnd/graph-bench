// Package micro registers the micro workload family (spec 06 §1): the engine
// microscope, one behavior per query. It calls workload.Register in its init
// so any binary that imports this package (blank or otherwise) gets the
// "micro-read", "micro-er", and "micro-mix" workloads in the registry.
//
// The read family runs on the synthetic generators so every reference is
// engine-independent: the point lookup, its negative variant, and the edge
// probe (the index paths); the k-hop expansions (the traversal atom); and the
// scan-and-aggregate pair (the columnar contrast). The triangle counts run on
// the ER graph, where a closed-form expectation cross-checks the oracle.
// Query IDs are carried from v1 verbatim — zu's primitive mode pattern-matches
// "micro-point", "micro-point-miss", "micro-khop1", and "micro-edge" — and the
// pool keys are the ones workload.Curate serves.
//
// Fidelity is "harness-native" (spec 07 §6): these queries mirror no external
// standard; they are the harness's own microscope.
//
// A note on the dialect texts below: both spell the labels the manifest
// declares, :Node and :EDGE. The zuQL texts used to say :node and :edge,
// because zu's loader named its two tables that way whatever the dataset
// called them, and the table load ended that: one zu node table per label,
// named after the label. A query with no zuQL text SKIPs on zu rather than
// falling back to the Cypher one.
package micro

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

func init() {
	workload.Register(microRead)
	workload.Register(microER)
	workload.Register(microPowerLaw)
	workload.Register(microUniform)
	workload.Register(microMix)
}

// Pool keys matching what workload.Curate serves. The families reference
// them; the runner curates and binds the pools before verification.
const (
	khopKey      = "micro-khop"
	spKey        = "micro-sp"
	pointKey     = "micro-point"
	pointMissKey = "micro-point-miss"
	edgeKey      = "micro-edge"
)

// microRead is the read family on the 4-neighbor grid, where every count is
// closed-form: the queries validate the oracle and exercise the engine's
// index, expand, and scan operators on the cleanest possible dataset.
var microRead = &workload.Workload{
	Name:     "micro-read",
	Title:    "Micro-benchmarks: point reads, k-hop expansions, scans on a grid",
	Family:   "micro",
	Dataset:  "grid-100x100",
	Fidelity: "harness-native",
	Queries: []*workload.Query{
		pointQuery,
		pointMissQuery,
		edgeQuery,
		khop1Query,
		khop2Query,
		khop3Query,
		varlenQuery,
		scanCountQuery,
		scanStatsQuery,
	},
}

// microPowerLaw and microUniform are the same traversal questions asked of two
// graphs with the same node count and wildly different degree distributions.
// The grid in micro-read is regular by construction: every seed has the same
// out-degree, so a k-hop number there says what an expansion costs on average
// and nothing about what it costs when the average is a lie. The power-law
// graph has hubs; the uniform graph has none. Running the identical query set
// over both turns "how fast is expansion" into "how fast is expansion, and how
// much does that answer depend on where you start", which is the question that
// separates engines with adjacency-list indexes from engines that scan.
//
// Power-law gets the full set including the path queries: shortest paths are
// where degree skew hurts most, because a frontier that touches a hub explodes.
var microPowerLaw = &workload.Workload{
	Name:     "micro-powerlaw",
	Title:    "Micro-benchmarks on a power-law graph (hub-heavy degree skew)",
	Family:   "micro",
	Dataset:  "powerlaw-10k",
	Fidelity: "harness-native",
	Queries: []*workload.Query{
		pointQuery,
		pointMissQuery,
		khop1Query,
		khop2Query,
		khop3Query,
		varlenQuery,
		spQuery,
		spBidirQuery,
		triangleDirectedQuery,
		triangleUndirectedQuery,
	},
}

// microUniform is the flat-degree control: same node count, same query set
// minus the parts that only pay off under skew. Its numbers are the baseline
// the power-law numbers are read against.
var microUniform = &workload.Workload{
	Name:     "micro-uniform",
	Title:    "Micro-benchmarks on a uniform-degree random graph",
	Family:   "micro",
	Dataset:  "uniform-10k",
	Fidelity: "harness-native",
	Queries: []*workload.Query{
		pointQuery,
		pointMissQuery,
		khop1Query,
		khop2Query,
		khop3Query,
		varlenQuery,
	},
}

// microER is the triangle family on an Erdős–Rényi graph, where the triangle
// count has a clean closed-form expectation for validation.
var microER = &workload.Workload{
	Name:     "micro-er",
	Title:    "Micro-benchmarks on an ER dataset (directed and undirected triangle counts)",
	Family:   "micro",
	Dataset:  "er-10k",
	Fidelity: "harness-native",
	Queries: []*workload.Query{
		triangleDirectedQuery,
		triangleUndirectedQuery,
	},
}

// microMix is the interleaved read mix over the micro-read queries: the same
// questions fired concurrently at a point-read-heavy blend, so per-class
// latency under contention can be compared against the isolation numbers.
// v1 ran the micro queries in isolation only; the weights here are the
// harness's own (point-heavy, scans rare), disclosed as such.
var microMix = &workload.Workload{
	Name:     "micro-mix",
	Title:    "Micro read mix (point-heavy blend of the micro-read queries)",
	Family:   "micro",
	Dataset:  "grid-100x100",
	Fidelity: "harness-native",
	// Listed out rather than reusing microRead.Queries: the mix is defined by
	// its weights, and a query in the workload with no weight never gets
	// scheduled. Sharing the slice means adding a query to micro-read silently
	// adds dead weight here.
	Queries: []*workload.Query{
		pointQuery,
		pointMissQuery,
		edgeQuery,
		khop1Query,
		khop2Query,
		scanCountQuery,
		scanStatsQuery,
	},
	Mix: &workload.Mix{
		Weights: map[string]float64{
			"micro-point":      35,
			"micro-point-miss": 10,
			"micro-edge":       15,
			"micro-khop1":      25,
			"micro-khop2":      10,
			"micro-scan-count": 3,
			"micro-scan-stats": 2,
		},
	},
}

// pointQuery is the indexed property probe: resolve one node by its id and
// return it. It is the cheapest read an engine does, the floor of the latency
// distribution. The pool holds only existing ids, so every draw returns a row.
var pointQuery = &workload.Query{
	ID:      "micro-point",
	Class:   engine.PointRead,
	PoolKey: pointKey,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (n:Node {id: $id}) RETURN n.id AS id`,
		engine.KuzuCy: `MATCH (n:Node {id: CAST($id AS INT64)}) RETURN n.id AS id`,
		engine.ZuQL:   `MATCH (n:Node {id: $id}) RETURN n.id AS id`,
	},
	Reference: &workload.RefStrategy{
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-point reference: %w", err)
			}
			id, ok := workload.Token(p, "id")
			if !ok {
				return nil, fmt.Errorf("micro-point: params missing id, got %T", p["id"])
			}
			if !g.HasNode(id) {
				return &workload.Answer{Columns: []string{"id"}, Rows: nil}, nil
			}
			n, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("micro-point: id %q is not an integer: %w", id, err)
			}
			return &workload.Answer{
				Columns: []string{"id"},
				Rows:    [][]engine.Value{{n}},
			}, nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// pointMissQuery is the negative lookup: probe an id that does not exist. It
// isolates the index miss path, which some engines short-circuit faster than
// a hit and some slower. The pool holds only absent ids, so the reference is
// always zero rows; a present id in the pool is a corrupt pool and an error.
var pointMissQuery = &workload.Query{
	ID:      "micro-point-miss",
	Class:   engine.PointRead,
	PoolKey: pointMissKey,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (n:Node {id: $id}) RETURN n.id AS id`,
		engine.KuzuCy: `MATCH (n:Node {id: CAST($id AS INT64)}) RETURN n.id AS id`,
		engine.ZuQL:   `MATCH (n:Node {id: $id}) RETURN n.id AS id`,
	},
	Reference: &workload.RefStrategy{
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-point-miss reference: %w", err)
			}
			id, ok := workload.Token(p, "id")
			if !ok {
				return nil, fmt.Errorf("micro-point-miss: params missing id, got %T", p["id"])
			}
			if g.HasNode(id) {
				return nil, fmt.Errorf("micro-point-miss: pool id %q exists; the miss pool is corrupt", id)
			}
			return &workload.Answer{Columns: []string{"id"}, Rows: nil}, nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// edgeQuery is the adjacency probe: does the directed edge (src, dst) exist.
// It is the other O(1) read — one adjacency check instead of one index probe
// — and the pool mixes existing and absent pairs so both answers are
// measured. The reference scans the relationship CSV, an adjacency probe over
// the same bytes the engine loaded.
//
// The result column is named "found", not the more natural "exists": EXISTS is
// a reserved keyword in Ladybug's parser, and aliasing to it fails the whole
// query at prepare time. One rejected alias fails verification, and a failed
// verification skips measurement for every query in the workload, so the
// spelling of one column name decides whether an engine reports any numbers at
// all.
var edgeQuery = &workload.Query{
	ID:      "micro-edge",
	Class:   engine.PointRead,
	PoolKey: edgeKey,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (a:Node {id: $src})-[:EDGE]->(b:Node {id: $dst}) RETURN count(*) > 0 AS found`,
		engine.KuzuCy: `MATCH (a:Node {id: CAST($src AS INT64)})-[:EDGE]->(b:Node {id: CAST($dst AS INT64)}) RETURN count(*) > 0 AS found`,
		// zu's binder only takes a bare aggregate call in a RETURN item, so
		// the comparison moves one stage down: count in a WITH, compare in
		// the RETURN. Same probe, same two rows of work, different spelling.
		engine.ZuQL: `MATCH (a:Node {id: $src})-[:EDGE]->(b:Node {id: $dst}) WITH count(*) AS c RETURN c > 0 AS found`,
	},
	Reference: &workload.RefStrategy{
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			src, _ := workload.Token(p, "src")
			dst, _ := workload.Token(p, "dst")
			if src == "" || dst == "" {
				return nil, fmt.Errorf("micro-edge: params missing src or dst, got %v", p)
			}
			exists, err := hasEdge(ds, src, dst)
			if err != nil {
				return nil, fmt.Errorf("micro-edge reference: %w", err)
			}
			return &workload.Answer{
				Columns: []string{"found"},
				Rows:    [][]engine.Value{{exists}},
			}, nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// khop1Query is the one-hop neighborhood expansion: follow one edge from the
// seed, count the neighbors. It isolates the single-expand operation and is
// the atom of traversal.
var khop1Query = &workload.Query{
	ID:      "micro-khop1",
	Class:   engine.Traversal,
	PoolKey: khopKey,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (a:Node {id: $seed})-[:EDGE]->(b:Node) RETURN count(b) AS n`,
		engine.KuzuCy: `MATCH (a:Node {id: CAST($seed AS INT64)})-[:EDGE]->(b:Node) RETURN count(b) AS n`,
		engine.ZuQL:   `MATCH (a:Node {id: $seed})-[:EDGE]->(b:Node) RETURN count(b) AS n`,
	},
	Reference: &workload.RefStrategy{
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-khop1 reference: %w", err)
			}
			seed, ok := workload.Token(p, "seed")
			if !ok {
				return nil, fmt.Errorf("micro-khop1: params missing seed, got %T", p["seed"])
			}
			return countAnswer(int64(g.OutDegree(seed))), nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// khop2Query is the two-hop expansion: distinct nodes reachable in exactly
// two hops from the seed. It exercises the engine's double-expand and
// distinct deduplication.
var khop2Query = &workload.Query{
	ID:      "micro-khop2",
	Class:   engine.Traversal,
	PoolKey: khopKey,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (a:Node {id: $seed})-[:EDGE]->()-[:EDGE]->(c:Node) RETURN count(DISTINCT c) AS n`,
		engine.KuzuCy: `MATCH (a:Node {id: CAST($seed AS INT64)})-[:EDGE]->()-[:EDGE]->(c:Node) RETURN count(DISTINCT c) AS n`,
		engine.ZuQL:   `MATCH (a:Node {id: $seed})-[:EDGE]->()-[:EDGE]->(c:Node) RETURN count(DISTINCT c) AS n`,
	},
	Reference: &workload.RefStrategy{
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-khop2 reference: %w", err)
			}
			seed, ok := workload.Token(p, "seed")
			if !ok {
				return nil, fmt.Errorf("micro-khop2: params missing seed, got %T", p["seed"])
			}
			return countAnswer(int64(g.ReachableExact(seed, 2))), nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// khop3Query is the three-hop expansion, the last depth in the k-hop sweep.
// Three hops is where a skewed graph stops looking like a flat one: on the
// grid the frontier grows linearly, on the power-law graph a seed that lands
// near a hub reaches most of the graph.
var khop3Query = &workload.Query{
	ID:      "micro-khop3",
	Class:   engine.Traversal,
	PoolKey: khopKey,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (a:Node {id: $seed})-[:EDGE]->()-[:EDGE]->()-[:EDGE]->(d:Node) RETURN count(DISTINCT d) AS n`,
		engine.KuzuCy: `MATCH (a:Node {id: CAST($seed AS INT64)})-[:EDGE]->()-[:EDGE]->()-[:EDGE]->(d:Node) RETURN count(DISTINCT d) AS n`,
		engine.ZuQL:   `MATCH (a:Node {id: $seed})-[:EDGE]->()-[:EDGE]->()-[:EDGE]->(d:Node) RETURN count(DISTINCT d) AS n`,
	},
	Reference: &workload.RefStrategy{
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-khop3 reference: %w", err)
			}
			seed, ok := workload.Token(p, "seed")
			if !ok {
				return nil, fmt.Errorf("micro-khop3: params missing seed, got %T", p["seed"])
			}
			return countAnswer(int64(g.ReachableExact(seed, 3))), nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// varlenQuery is the variable-length 1-to-3-hop expansion: the union of nodes
// reachable in one, two, or three hops. Where the fixed-depth k-hop queries
// measure one expansion repeated, this measures the engine's variable-length
// operator and its frontier union, which are usually different code.
var varlenQuery = &workload.Query{
	ID:      "micro-varlen",
	Class:   engine.Traversal,
	PoolKey: khopKey,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (a:Node {id: $seed})-[:EDGE*1..3]->(c:Node) RETURN count(DISTINCT c) AS n`,
		engine.KuzuCy: `MATCH (a:Node {id: CAST($seed AS INT64)})-[:EDGE*1..3]->(c:Node) RETURN count(DISTINCT c) AS n`,
		engine.ZuQL:   `MATCH (a:Node {id: $seed})-[:EDGE*1..3]->(c:Node) RETURN count(DISTINCT c) AS n`,
	},
	Reference: &workload.RefStrategy{
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-varlen reference: %w", err)
			}
			seed, ok := workload.Token(p, "seed")
			if !ok {
				return nil, fmt.Errorf("micro-varlen: params missing seed, got %T", p["seed"])
			}
			return countAnswer(int64(g.ReachableRange(seed, 1, 3))), nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// spQuery is the single-pair shortest path: the length of the shortest
// directed path from src to dst. The pool draws pairs at a spread of true
// distances, so the measurement covers near and far. A pair with no path
// returns no row, which is the answer, not an error.
var spQuery = &workload.Query{
	ID:                "micro-sp",
	Class:             engine.Traversal,
	PoolKey:           spKey,
	NeedsShortestPath: true,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH p = shortestPath((a:Node {id: $src})-[:EDGE*]->(b:Node {id: $dst})) RETURN length(p) AS d`,
		// Kuzu has no shortestPath(). It spells the same question with the
		// SHORTEST keyword on a variable-length pattern, and the upper bound
		// has to be left off: Kuzu caps a bounded variable-length relationship
		// at 30 hops, and an unbounded 1.. removes the cap.
		engine.KuzuCy: `MATCH (a:Node {id: CAST($src AS INT64)})-[r:EDGE* SHORTEST 1..]->(b:Node {id: CAST($dst AS INT64)}) RETURN length(r) AS d`,
		// zu writes the selector in front of the pattern, GQL style, and
		// counts the hops off the rel list rather than a path variable.
		// ANY SHORTEST is the right one here: the question is the distance,
		// so one minimum-hop path per endpoint answers it and enumerating
		// the rest would be work nobody reads.
		engine.ZuQL: `MATCH ANY SHORTEST (a:Node {id: $src})-[r:EDGE*]->(b:Node {id: $dst}) RETURN size(r) AS d`,
	},
	Reference: &workload.RefStrategy{
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-sp reference: %w", err)
			}
			src, _ := workload.Token(p, "src")
			dst, _ := workload.Token(p, "dst")
			if src == "" || dst == "" {
				return nil, fmt.Errorf("micro-sp: params missing src or dst, got %v", p)
			}
			d, ok := g.ShortestPath(src, dst)
			if !ok {
				return &workload.Answer{Columns: []string{"d"}, Rows: nil}, nil
			}
			return &workload.Answer{
				Columns: []string{"d"},
				Rows:    [][]engine.Value{{int64(d)}},
			}, nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// spBidirQuery is the same question over undirected edges, which is what a
// meet-in-the-middle search answers by growing two frontiers instead of one.
// It draws from the same pool as micro-sp, so the two rows sit side by side
// and the difference is the search strategy rather than the input. Class
// Subgraph because the two-frontier meet is a join, not a single expand.
var spBidirQuery = &workload.Query{
	ID:                "micro-sp-bidir",
	Class:             engine.Subgraph,
	PoolKey:           spKey,
	NeedsShortestPath: true,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH p = shortestPath((a:Node {id: $src})-[:EDGE*]-(b:Node {id: $dst})) RETURN length(p) AS d`,
		engine.KuzuCy: `MATCH (a:Node {id: CAST($src AS INT64)})-[r:EDGE* SHORTEST 1..]-(b:Node {id: CAST($dst AS INT64)}) RETURN length(r) AS d`,
		engine.ZuQL:   `MATCH ANY SHORTEST (a:Node {id: $src})-[r:EDGE*]-(b:Node {id: $dst}) RETURN size(r) AS d`,
	},
	Reference: &workload.RefStrategy{
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-sp-bidir reference: %w", err)
			}
			src, _ := workload.Token(p, "src")
			dst, _ := workload.Token(p, "dst")
			if src == "" || dst == "" {
				return nil, fmt.Errorf("micro-sp-bidir: params missing src or dst, got %v", p)
			}
			d, ok := g.ShortestPathUndirected(src, dst)
			if !ok {
				return &workload.Answer{Columns: []string{"d"}, Rows: nil}, nil
			}
			return &workload.Answer{
				Columns: []string{"d"},
				Rows:    [][]engine.Value{{int64(d)}},
			}, nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// scanCountQuery is the whole-population count: the cheapest full scan an
// engine does, with no property access at all. It takes no parameters.
var scanCountQuery = &workload.Query{
	ID:    "micro-scan-count",
	Class: engine.Aggregation,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (n:Node) RETURN count(n) AS n`,
		engine.ZuQL:   `MATCH (n:Node) RETURN count(n) AS n`,
	},
	Params: workload.Fixed{},
	Reference: &workload.RefStrategy{
		Compute: func(ds engine.Dataset, _ workload.Params) (*workload.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-scan-count reference: %w", err)
			}
			return countAnswer(int64(g.NodeCount())), nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// scanStatsQuery is the property scan and aggregate: read one column across
// the whole node population and aggregate it, with no traversal. It is the
// columnar-storage showcase, the "analytical-lite" contrast to the
// pointer-chasing reads. On the synthetic datasets it aggregates the dense
// numeric id column.
var scanStatsQuery = &workload.Query{
	ID:    "micro-scan-stats",
	Class: engine.Aggregation,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (n:Node) RETURN count(n) AS n, avg(n.id) AS avgId`,
		engine.ZuQL:   `MATCH (n:Node) RETURN count(n) AS n, avg(n.id) AS avgId`,
	},
	Params: workload.Fixed{},
	Reference: &workload.RefStrategy{
		Compute: func(ds engine.Dataset, _ workload.Params) (*workload.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-scan-stats reference: %w", err)
			}
			count, sum := g.ScanIDStats()
			var avg float64
			if count > 0 {
				avg = float64(sum) / float64(count)
			}
			return &workload.Answer{
				Columns: []string{"n", "avgId"},
				Rows:    [][]engine.Value{{count, avg}},
			}, nil
		},
		// avg is a float; coerce numbers and use the default float tolerance
		// so an engine that returns the count as a float still validates.
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// triangleDirectedQuery counts directed 3-cycles a->b->c->a over the whole
// graph. It is the WCOJ showcase: a binary-join plan materializes all 2-paths
// while a worst-case-optimal join intersects adjacency lists.
var triangleDirectedQuery = &workload.Query{
	ID:    "micro-triangle",
	Class: engine.Subgraph,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (a:Node)-[:EDGE]->(b:Node)-[:EDGE]->(c:Node)-[:EDGE]->(a) RETURN count(*) AS n`,
		engine.ZuQL:   `MATCH (a:Node)-[:EDGE]->(b:Node)-[:EDGE]->(c:Node)-[:EDGE]->(a) RETURN count(*) AS n`,
	},
	Params: workload.Fixed{},
	Reference: &workload.RefStrategy{
		Compute: func(ds engine.Dataset, _ workload.Params) (*workload.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-triangle reference: %w", err)
			}
			// count(*) over the directed 3-cycle pattern has no ordering
			// constraint, so each distinct directed triangle is matched once
			// per rotation (3 times: a->b->c, b->c->a, c->a->b).
			return countAnswer(3 * g.DirectedTriangles()), nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// triangleUndirectedQuery counts undirected triangles (each unordered triple
// once) in the underlying undirected graph.
//
// The count is over distinct node triples, not over pattern matches, because
// the underlying graph is directed: a pair joined by edges in both directions
// is one undirected adjacency but two relationships, so an unordered triple
// with reciprocal edges matches the pattern 2, 4, or 8 times. Counting matches
// answers a different question than the GAP triangle kernel asks, and it
// answers it differently for every dataset's reciprocity rate.
//
// The ordering is over the id property rather than the engine's internal
// element id, so the query asks the same question of every engine instead of
// depending on how each one numbers its nodes.
var triangleUndirectedQuery = &workload.Query{
	ID:    "micro-triangle-undirected",
	Class: engine.Subgraph,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (a:Node)-[:EDGE]-(b:Node)-[:EDGE]-(c:Node)-[:EDGE]-(a) WHERE a.id < b.id AND b.id < c.id RETURN count(DISTINCT [a.id, b.id, c.id]) AS n`,
		engine.ZuQL:   `MATCH (a:Node)-[:EDGE]-(b:Node)-[:EDGE]-(c:Node)-[:EDGE]-(a) WHERE a.id < b.id AND b.id < c.id RETURN count(DISTINCT [a.id, b.id, c.id]) AS n`,
	},
	Params: workload.Fixed{},
	Reference: &workload.RefStrategy{
		Compute: func(ds engine.Dataset, _ workload.Params) (*workload.Answer, error) {
			g, err := workload.LoadGraph(ds)
			if err != nil {
				return nil, fmt.Errorf("micro-triangle-undirected reference: %w", err)
			}
			return countAnswer(g.UndirectedTriangles()), nil
		},
		Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
	},
}

// countAnswer wraps a single integer in the canonical one-row answer every
// count query returns.
func countAnswer(n int64) *workload.Answer {
	return &workload.Answer{
		Columns: []string{"n"},
		Rows:    [][]engine.Value{{n}},
	}
}

// hasEdge reports whether the directed edge src->dst appears in any of the
// dataset's relationship files, locating the endpoint columns through the
// typed CSV header (spec 05 §1). It is the micro-edge oracle: an adjacency
// probe over the canonical bytes, never the engine under test.
func hasEdge(ds engine.Dataset, src, dst string) (bool, error) {
	for typ := range ds.Schema().Rels {
		files, err := ds.RelFiles(typ)
		if err != nil {
			return false, fmt.Errorf("rel files for %q: %w", typ, err)
		}
		for _, f := range files {
			found, err := fileHasEdge(f, src, dst)
			if err != nil {
				return false, err
			}
			if found {
				return true, nil
			}
		}
	}
	return false, nil
}

// fileHasEdge scans one relationship CSV for the (src, dst) pair.
func fileHasEdge(path, src, dst string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.ReuseRecord = true
	startCol, endCol := -1, -1
	first := true
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("read %s: %w", path, err)
		}
		if first {
			first = false
			for i, cell := range rec {
				switch {
				case strings.HasSuffix(cell, ":START_ID"):
					startCol = i
				case strings.HasSuffix(cell, ":END_ID"):
					endCol = i
				}
			}
			if startCol < 0 || endCol < 0 {
				return false, fmt.Errorf("rel file %s header has no :START_ID/:END_ID columns", path)
			}
			continue
		}
		if startCol < len(rec) && endCol < len(rec) && rec[startCol] == src && rec[endCol] == dst {
			return true, nil
		}
	}
}
