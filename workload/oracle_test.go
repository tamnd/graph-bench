package workload

import (
	"context"
	"encoding/csv"
	"io"
	"os"
	"strconv"
	"testing"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/target"
)

// genDataset generates a synthetic dataset into a temp directory and opens it, so
// the oracle runs against real canonical CSV the same way the validator will.
func genDataset(t *testing.T, cfg gen.Config) target.Dataset {
	t.Helper()
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, err := gen.Generate(context.Background(), cfg, w); err != nil {
		t.Fatalf("Generate(%+v): %v", cfg, err)
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ds
}

// TestOracleGridClosedForm cross-checks the oracle against the grid's closed-form
// invariants, the cheapest validation there is: the answers are arithmetic in the
// dimensions, so a divergence is the oracle's fault, not a second engine's. The
// 3x3 4-neighbor grid has directed edges that only go right and down, so it is a
// DAG with a known diameter and no triangles.
func TestOracleGridClosedForm(t *testing.T) {
	ds := genDataset(t, gen.Config{Kind: "grid", Rows: 3, Cols: 3})
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

	// id(r,c) = r*3 + c. Node 0 (top-left) has a right and a down edge; node 8
	// (bottom-right) has neither.
	if d := g.OutDegree("0"); d != 2 {
		t.Errorf("OutDegree(0) = %d, want 2", d)
	}
	if d := g.OutDegree("8"); d != 0 {
		t.Errorf("OutDegree(8) = %d, want 0", d)
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

	// Corner to corner is the Manhattan distance, which is the grid's diameter.
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

	// A right/down DAG has no directed cycles, and a 4-neighbor grid is bipartite,
	// so both triangle counts are zero, matching the generator's invariant.
	if c := g.DirectedTriangles(); c != 0 {
		t.Errorf("DirectedTriangles = %d, want 0", c)
	}
	if c := g.UndirectedTriangles(); c != 0 {
		t.Errorf("UndirectedTriangles = %d, want 0", c)
	}
	if inv := ds.Manifest().Invariants.TriangleCount; inv != nil && *inv != 0 {
		t.Errorf("grid invariant TriangleCount = %d, want 0", *inv)
	}
}

// TestOracleTriangleVsBruteForce cross-checks the oracle's triangle counts on a
// random ER graph against an independent brute-force count read straight from the
// CSV in the test. Two implementations agreeing on a graph with hundreds of
// triangles is the real evidence the intersection method is right; the grid only
// proves the zero case.
func TestOracleTriangleVsBruteForce(t *testing.T) {
	ds := genDataset(t, gen.Config{Kind: "er", N: 60, P: 0.1, Seed: 7})
	g, err := LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}

	edges := readEdges(t, ds)
	if len(edges) == 0 {
		t.Fatal("ER produced no edges; pick parameters that yield some")
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

// TestOracleReachableVsBruteForce cross-checks k-hop reachability on the ER graph
// against an independent set-expansion done from the same edge list, so the
// breadth-first frontier walk is validated on a graph with branching, not just on
// the grid's tidy lattice.
func TestOracleReachableVsBruteForce(t *testing.T) {
	ds := genDataset(t, gen.Config{Kind: "er", N: 60, P: 0.1, Seed: 7})
	g, err := LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	adj := adjacency(readEdges(t, ds))

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
// endpoint is not a node, so a corrupt dataset is a load error, not a silently
// wrong reference.
func TestLoadGraphRejectsDanglingEdge(t *testing.T) {
	ds := genDataset(t, gen.Config{Kind: "grid", Rows: 3, Cols: 3})
	// Append an edge to a nonexistent node id directly in the rels file.
	files, _, err := ds.RelFiles("EDGE")
	if err != nil || len(files) == 0 {
		t.Fatalf("RelFiles: %v (files=%v)", err, files)
	}
	f, err := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open rel file: %v", err)
	}
	if _, err := f.WriteString("0,999999,EDGE\n"); err != nil {
		t.Fatalf("write dangling edge: %v", err)
	}
	_ = f.Close()

	if _, err := LoadGraph(ds); err == nil {
		t.Fatal("LoadGraph accepted a dangling edge, want an error")
	}
}

// readEdges reads every relationship file in the dataset into a slice of
// start/end id pairs, independently of the oracle's own CSV reader, so a test can
// recount from the same source the oracle used.
func readEdges(t *testing.T, ds target.Dataset) [][2]string {
	t.Helper()
	var edges [][2]string
	for typ := range ds.Schema().Relationships {
		files, cols, err := ds.RelFiles(typ)
		if err != nil {
			t.Fatalf("RelFiles(%s): %v", typ, err)
		}
		var startCol, endCol = -1, -1
		for i, c := range cols {
			switch c.Type {
			case "START_ID":
				startCol = i
			case "END_ID":
				endCol = i
			}
		}
		if startCol < 0 || endCol < 0 {
			t.Fatalf("rel %s header missing endpoints: %v", typ, cols)
		}
		for _, path := range files {
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("open %s: %v", path, err)
			}
			r := csv.NewReader(f)
			r.FieldsPerRecord = -1
			first := true
			for {
				rec, err := r.Read()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("read %s: %v", path, err)
				}
				if first {
					first = false
					continue
				}
				edges = append(edges, [2]string{rec[startCol], rec[endCol]})
			}
			_ = f.Close()
		}
	}
	return edges
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

// bfExactSet returns the set of nodes reachable in exactly k forward hops from
// the seed, by repeated frontier expansion: an independent reimplementation of
// ReachableExact for the cross-check.
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

// bruteForceDirectedTriangles counts distinct directed 3-cycles by scanning every
// ordered pair of edges that chains a->b->c and checking for the closing edge
// c->a, then dividing by the three rotations. It is the O(E*deg) naive method,
// deliberately unlike the oracle's intersection method.
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

// bruteForceUndirectedTriangles counts unordered triangles by sorting the ids
// numerically and counting triples a<b<c with all three undirected edges present.
func bruteForceUndirectedTriangles(edges [][2]string) int64 {
	adj := map[string]map[string]struct{}{}
	link := func(x, y string) {
		if adj[x] == nil {
			adj[x] = map[string]struct{}{}
		}
		adj[x][y] = struct{}{}
	}
	nodeSet := map[string]struct{}{}
	for _, e := range edges {
		if e[0] == e[1] {
			continue
		}
		link(e[0], e[1])
		link(e[1], e[0])
		nodeSet[e[0]] = struct{}{}
		nodeSet[e[1]] = struct{}{}
	}
	ids := make([]int, 0, len(nodeSet))
	for id := range nodeSet {
		n, _ := strconv.Atoi(id)
		ids = append(ids, n)
	}
	// Simple insertion-free sort: ids are small, use the standard library.
	sortInts(ids)
	connected := func(x, y int) bool {
		_, ok := adj[strconv.Itoa(x)][strconv.Itoa(y)]
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

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}
