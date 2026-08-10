package workload

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tamnd/graph-bench/engine"
)

// The oracle tests build canonical-layout datasets by hand in a temp dir (the
// dataset package is a separate v0.3.0 work item), so LoadGraph runs against
// real CSV files with typed headers the same way the validator will.

// testDS is a minimal engine.Dataset over a hand-written canonical directory.
type testDS struct {
	dir    string
	schema engine.Schema
}

var _ engine.Dataset = (*testDS)(nil)

func (d *testDS) Name() string               { return "test" }
func (d *testDS) Checksum() string           { return "" }
func (d *testDS) Dir() string                { return d.dir }
func (d *testDS) Manifest() *engine.Manifest { return nil }
func (d *testDS) Schema() engine.Schema      { return d.schema }
func (d *testDS) Statements() []string       { return nil }

func (d *testDS) Params(key string) ([]map[string]engine.Value, error) { return nil, nil }

func (d *testDS) NodeFiles(label string) ([]string, error) {
	return d.schema.Nodes[label].Files, nil
}

func (d *testDS) RelFiles(typ string) ([]string, error) {
	return d.schema.Rels[typ].Files, nil
}

// writeDataset writes a single-label ("Node"), single-type ("EDGE") canonical
// dataset: node ids 0..n-1 under nodes/Node.csv and the given relationship
// rows under rels/EDGE.csv with the given typed header.
func writeDataset(t *testing.T, n int, relHeader string, relRows [][]string) *testDS {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"nodes", "rels"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	var nb strings.Builder
	nb.WriteString("id:ID\n")
	for i := 0; i < n; i++ {
		nb.WriteString(strconv.Itoa(i))
		nb.WriteByte('\n')
	}
	nodeFile := filepath.Join(dir, "nodes", "Node.csv")
	if err := os.WriteFile(nodeFile, []byte(nb.String()), 0o644); err != nil {
		t.Fatalf("write nodes: %v", err)
	}
	var rb strings.Builder
	rb.WriteString(relHeader)
	rb.WriteByte('\n')
	for _, r := range relRows {
		rb.WriteString(strings.Join(r, ","))
		rb.WriteByte('\n')
	}
	relFile := filepath.Join(dir, "rels", "EDGE.csv")
	if err := os.WriteFile(relFile, []byte(rb.String()), 0o644); err != nil {
		t.Fatalf("write rels: %v", err)
	}
	return &testDS{
		dir: dir,
		schema: engine.Schema{
			Nodes: map[string]engine.NodeSchema{
				"Node": {Files: []string{nodeFile}, ID: engine.Column{Name: "id", Type: "ID"}},
			},
			Rels: map[string]engine.RelSchema{
				"EDGE": {Files: []string{relFile}, Start: "Node", End: "Node"},
			},
		},
	}
}

// gridEdges returns the right/down directed edges of a rows x cols lattice
// with id(r,c) = r*cols + c, the closed-form fixture (a DAG, bipartite, no
// triangles, diameter rows+cols-2).
func gridEdges(rows, cols int) [][2]string {
	var edges [][2]string
	id := func(r, c int) string { return strconv.Itoa(r*cols + c) }
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			if c+1 < cols {
				edges = append(edges, [2]string{id(r, c), id(r, c+1)})
			}
			if r+1 < rows {
				edges = append(edges, [2]string{id(r, c), id(r+1, c)})
			}
		}
	}
	return edges
}

// erEdges returns a deterministic pseudo-random directed edge list: each
// ordered pair (i,j), i != j, is an edge with probability p, drawn from a
// fixed-seed LCG so the fixture is identical on every run.
func erEdges(n int, p float64, seed uint64) [][2]string {
	state := seed
	rnd := func() float64 {
		state = state*6364136223846793005 + 1442695040888963407
		return float64(state>>11) / float64(uint64(1)<<53)
	}
	var edges [][2]string
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			if i != j && rnd() < p {
				edges = append(edges, [2]string{strconv.Itoa(i), strconv.Itoa(j)})
			}
		}
	}
	return edges
}

// relRows renders an edge list as CSV rows for writeDataset.
func relRows(edges [][2]string) [][]string {
	rows := make([][]string, len(edges))
	for i, e := range edges {
		rows[i] = []string{e[0], e[1]}
	}
	return rows
}

// TestOracleGridClosedForm cross-checks the oracle against the grid's
// closed-form invariants, the cheapest validation there is: the answers are
// arithmetic in the dimensions, so a divergence is the oracle's fault, not a
// second engine's. The 3x3 4-neighbor grid has directed edges that only go
// right and down, so it is a DAG with a known diameter and no triangles.
func TestOracleGridClosedForm(t *testing.T) {
	ds := writeDataset(t, 9, ":START_ID,:END_ID", relRows(gridEdges(3, 3)))
	g, err := LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}

	if g.NodeCount() != 9 {
		t.Errorf("NodeCount = %d, want 9", g.NodeCount())
	}
	if g.EdgeCount() != 12 {
		t.Errorf("EdgeCount = %d, want 12", g.EdgeCount())
	}

	// id(r,c) = r*3 + c. Node 0 (top-left) has a right and a down edge; node
	// 8 (bottom-right) has neither.
	if d := g.OutDegree("0"); d != 2 {
		t.Errorf("OutDegree(0) = %d, want 2", d)
	}
	if d := g.OutDegree("8"); d != 0 {
		t.Errorf("OutDegree(8) = %d, want 0", d)
	}
	if !g.HasNode("4") || g.HasNode("9") {
		t.Error("HasNode: want true for 4, false for 9")
	}

	// From node 0: one hop reaches {1,3}; exactly two hops reach {2,4,6}.
	if r := g.ReachableExact("0", 1); r != 2 {
		t.Errorf("ReachableExact(0,1) = %d, want 2", r)
	}
	if r := g.ReachableExact("0", 2); r != 3 {
		t.Errorf("ReachableExact(0,2) = %d, want 3", r)
	}
	// One-to-two hops is the union {1,2,3,4,6}.
	if r := g.ReachableRange("0", 1, 2); r != 5 {
		t.Errorf("ReachableRange(0,1,2) = %d, want 5", r)
	}

	// Corner to corner is the Manhattan distance, the grid's diameter.
	if d, ok := g.ShortestPath("0", "8"); !ok || d != 4 {
		t.Errorf("ShortestPath(0,8) = %d,%v, want 4,true", d, ok)
	}
	// The reverse is unreachable in a right/down DAG.
	if _, ok := g.ShortestPath("8", "0"); ok {
		t.Error("ShortestPath(8,0) reported reachable, want unreachable")
	}
	// A node reaches itself at distance zero.
	if d, ok := g.ShortestPath("4", "4"); !ok || d != 0 {
		t.Errorf("ShortestPath(4,4) = %d,%v, want 0,true", d, ok)
	}
	// Undirected, the reverse corner walk is the same Manhattan distance.
	if d, ok := g.ShortestPathUndirected("8", "0"); !ok || d != 4 {
		t.Errorf("ShortestPathUndirected(8,0) = %d,%v, want 4,true", d, ok)
	}

	// ScanIDStats over ids 0..8: count 9, sum 36.
	if c, s := g.ScanIDStats(); c != 9 || s != 36 {
		t.Errorf("ScanIDStats = %d,%d, want 9,36", c, s)
	}

	// A right/down DAG has no directed cycles, and a 4-neighbor grid is
	// bipartite, so both triangle counts are zero.
	if c := g.DirectedTriangles(); c != 0 {
		t.Errorf("DirectedTriangles = %d, want 0", c)
	}
	if c := g.UndirectedTriangles(); c != 0 {
		t.Errorf("UndirectedTriangles = %d, want 0", c)
	}
}

// TestOracleTriangleVsBruteForce cross-checks the oracle's triangle counts on
// a pseudo-random graph against an independent brute-force count over the
// same edge list. Two implementations agreeing on a graph with hundreds of
// triangles is the real evidence the intersection method is right; the grid
// only proves the zero case.
func TestOracleTriangleVsBruteForce(t *testing.T) {
	edges := erEdges(60, 0.1, 7)
	if len(edges) == 0 {
		t.Fatal("ER produced no edges; pick parameters that yield some")
	}
	ds := writeDataset(t, 60, ":START_ID,:END_ID", relRows(edges))
	g, err := LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}

	wantDirected := bruteForceDirectedTriangles(edges)
	if wantDirected == 0 {
		t.Fatal("ER produced no directed triangles; raise N or P for a real cross-check")
	}
	if got := g.DirectedTriangles(); got != wantDirected {
		t.Errorf("DirectedTriangles = %d, want %d (brute force)", got, wantDirected)
	}

	wantUndirected := bruteForceUndirectedTriangles(edges)
	if wantUndirected == 0 {
		t.Fatal("ER produced no undirected triangles; raise N or P for a real cross-check")
	}
	if got := g.UndirectedTriangles(); got != wantUndirected {
		t.Errorf("UndirectedTriangles = %d, want %d (brute force)", got, wantUndirected)
	}
}

// TestOracleReachableVsBruteForce cross-checks k-hop reachability on the
// pseudo-random graph against an independent set-expansion done from the same
// edge list, so the breadth-first frontier walk is validated on a graph with
// branching, not just on the grid's tidy lattice.
func TestOracleReachableVsBruteForce(t *testing.T) {
	edges := erEdges(60, 0.1, 7)
	ds := writeDataset(t, 60, ":START_ID,:END_ID", relRows(edges))
	g, err := LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	adj := adjacency(edges)

	seed := "0"
	for k := 0; k <= 3; k++ {
		if got, want := g.ReachableExact(seed, k), len(bfExactSet(adj, seed, k)); got != want {
			t.Errorf("ReachableExact(%s,%d) = %d, want %d", seed, k, got, want)
		}
	}
	if got, want := g.ReachableRange(seed, 1, 3), len(bfRangeSet(adj, seed, 1, 3)); got != want {
		t.Errorf("ReachableRange(%s,1,3) = %d, want %d", seed, got, want)
	}
}

// TestLoadGraphRejectsDanglingEdge proves the loader refuses an edge whose
// endpoint is not a node, so a corrupt dataset is a load error, not a
// silently wrong reference.
func TestLoadGraphRejectsDanglingEdge(t *testing.T) {
	rows := relRows(gridEdges(3, 3))
	rows = append(rows, []string{"0", "999999"})
	ds := writeDataset(t, 9, ":START_ID,:END_ID", rows)
	if _, err := LoadGraph(ds); err == nil {
		t.Fatal("LoadGraph accepted a dangling edge, want an error")
	}
}

// TestLoadGraphRejectsMissingEndpoints proves a rel header without structural
// endpoint columns is a load error.
func TestLoadGraphRejectsMissingEndpoints(t *testing.T) {
	ds := writeDataset(t, 2, "a,b", [][]string{{"0", "1"}})
	if _, err := LoadGraph(ds); err == nil {
		t.Fatal("LoadGraph accepted a rel file with no :START_ID/:END_ID, want an error")
	}
}

// TestLoadGraphWeights proves the loader captures the optional "w" edge
// weight column (spec 07 §4–5: GAP/Graph500 weighted graphs) and that a
// dataset without the column loads with unit weights.
func TestLoadGraphWeights(t *testing.T) {
	// 0->1 costs 5 directly but 2 via 2; the weighted distances prove the
	// weights, not the hop counts, drive the answer.
	ds := writeDataset(t, 3, ":START_ID,:END_ID,w:INT64", [][]string{
		{"0", "1", "5"},
		{"0", "2", "1"},
		{"2", "1", "1"},
	})
	g, err := LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	dists, ok := g.SSSPWeighted("0")
	if !ok {
		t.Fatal("SSSPWeighted(0) not ok")
	}
	want := map[string]float64{"0": 0, "1": 2, "2": 1}
	if len(dists) != len(want) {
		t.Fatalf("len(dists) = %d, want %d", len(dists), len(want))
	}
	for _, d := range dists {
		if d.Val != want[d.ID] {
			t.Errorf("weighted dist to %s = %g, want %g", d.ID, d.Val, want[d.ID])
		}
	}

	// Without a "w" column every edge weighs 1, so the weighted distance is
	// the BFS level.
	unweighted := writeDataset(t, 9, ":START_ID,:END_ID", relRows(gridEdges(3, 3)))
	ug, err := LoadGraph(unweighted)
	if err != nil {
		t.Fatalf("LoadGraph (unweighted): %v", err)
	}
	levels, _ := ug.BFSLevels("0")
	wdists, _ := ug.SSSPWeighted("0")
	if len(levels) != len(wdists) {
		t.Fatalf("len mismatch: bfs %d, weighted sssp %d", len(levels), len(wdists))
	}
	for i := range levels {
		if levels[i].ID != wdists[i].ID || float64(levels[i].Val) != wdists[i].Val {
			t.Errorf("row %d: bfs %v, weighted sssp %v", i, levels[i], wdists[i])
		}
	}
}

// TestLoadGraphRejectsBadWeight proves a non-integer weight is a load error,
// same policy as a dangling edge: corrupt data fails loudly.
func TestLoadGraphRejectsBadWeight(t *testing.T) {
	ds := writeDataset(t, 2, ":START_ID,:END_ID,w:INT64", [][]string{{"0", "1", "heavy"}})
	if _, err := LoadGraph(ds); err == nil {
		t.Fatal("LoadGraph accepted a non-integer weight, want an error")
	}
}

// adjacency turns an edge list into a forward-neighbor set keyed by id token.
func adjacency(edges [][2]string) map[string]map[string]struct{} {
	adj := map[string]map[string]struct{}{}
	for _, e := range edges {
		if adj[e[0]] == nil {
			adj[e[0]] = map[string]struct{}{}
		}
		adj[e[0]][e[1]] = struct{}{}
	}
	return adj
}

// bfExactSet returns the set of nodes reachable in exactly k forward hops
// from the seed, by repeated frontier expansion: an independent
// reimplementation of ReachableExact for the cross-check.
func bfExactSet(adj map[string]map[string]struct{}, seed string, k int) map[string]struct{} {
	frontier := map[string]struct{}{seed: {}}
	for hop := 0; hop < k; hop++ {
		next := map[string]struct{}{}
		for u := range frontier {
			for v := range adj[u] {
				next[v] = struct{}{}
			}
		}
		frontier = next
	}
	return frontier
}

// bfRangeSet returns the union of the exact-depth sets over [lo,hi].
func bfRangeSet(adj map[string]map[string]struct{}, seed string, lo, hi int) map[string]struct{} {
	union := map[string]struct{}{}
	for k := lo; k <= hi; k++ {
		for u := range bfExactSet(adj, seed, k) {
			union[u] = struct{}{}
		}
	}
	return union
}

// bruteForceDirectedTriangles counts distinct directed 3-cycles by scanning
// every ordered pair of edges that chains a->b->c and checking for the
// closing edge c->a, then dividing by the three rotations. It is the O(E*deg)
// naive method, deliberately unlike the oracle's intersection method.
func bruteForceDirectedTriangles(edges [][2]string) int64 {
	out := map[string]map[string]struct{}{}
	has := map[[2]string]struct{}{}
	for _, e := range edges {
		if out[e[0]] == nil {
			out[e[0]] = map[string]struct{}{}
		}
		out[e[0]][e[1]] = struct{}{}
		has[[2]string{e[0], e[1]}] = struct{}{}
	}
	var raw int64
	for a := range out {
		for b := range out[a] {
			for c := range out[b] {
				if _, ok := has[[2]string{c, a}]; ok {
					raw++
				}
			}
		}
	}
	return raw / 3
}

// bruteForceUndirectedTriangles counts unordered triangles by numeric id
// order, counting triples a<b<c with all three undirected edges present.
func bruteForceUndirectedTriangles(edges [][2]string) int64 {
	adj := map[[2]string]struct{}{}
	nodeSet := map[string]struct{}{}
	for _, e := range edges {
		if e[0] == e[1] {
			continue
		}
		adj[[2]string{e[0], e[1]}] = struct{}{}
		adj[[2]string{e[1], e[0]}] = struct{}{}
		nodeSet[e[0]] = struct{}{}
		nodeSet[e[1]] = struct{}{}
	}
	var ids []int
	for id := range nodeSet {
		n, _ := strconv.Atoi(id)
		ids = append(ids, n)
	}
	connected := func(x, y int) bool {
		_, ok := adj[[2]string{strconv.Itoa(x), strconv.Itoa(y)}]
		return ok
	}
	var count int64
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if !connected(ids[i], ids[j]) {
				continue
			}
			for k := j + 1; k < len(ids); k++ {
				if connected(ids[i], ids[k]) && connected(ids[j], ids[k]) {
					count++
				}
			}
		}
	}
	return count
}
