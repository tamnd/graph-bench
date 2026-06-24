package workload

import (
	"math"
	"strconv"
	"testing"

	"github.com/tamnd/graph-bench/dataset/gen"
)

// TestBFSLevelsGrid checks BFS levels against the grid's closed form. On a 3x3
// right/down DAG the level of node id = r*3+c from node 0 is the Manhattan
// distance r+c, and every node is reachable.
func TestBFSLevelsGrid(t *testing.T) {
	ds := genDataset(t, gen.Config{Kind: "grid", Rows: 3, Cols: 3})
	g, err := LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	levels, ok := g.BFSLevels("0")
	if !ok {
		t.Fatal("BFSLevels(0) not ok")
	}
	if len(levels) != 9 {
		t.Errorf("len(levels) = %d, want 9 (grid is reachable from corner 0)", len(levels))
	}
	got := map[string]int64{}
	for _, l := range levels {
		got[l.ID] = l.Val
	}
	for id := 0; id < 9; id++ {
		r, c := id/3, id%3
		want := int64(r + c)
		if got[strconv.Itoa(id)] != want {
			t.Errorf("level of %d = %d, want %d", id, got[strconv.Itoa(id)], want)
		}
	}
}

// TestSSSPUnitMatchesBFS checks the unit-weight SSSP equals the BFS level in float
// form, the relationship the oracle relies on for the unweighted synthetic graph.
func TestSSSPUnitMatchesBFS(t *testing.T) {
	ds := genDataset(t, gen.Config{Kind: "er", N: 50, P: 0.08, Seed: 3})
	g, err := LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	levels, _ := g.BFSLevels("0")
	dists, _ := g.SSSPUnit("0")
	if len(levels) != len(dists) {
		t.Fatalf("len mismatch: bfs %d, sssp %d", len(levels), len(dists))
	}
	for i := range levels {
		if levels[i].ID != dists[i].ID || float64(levels[i].Val) != dists[i].Val {
			t.Errorf("row %d: bfs %v, sssp %v", i, levels[i], dists[i])
		}
	}
}

// TestPageRankInvariants checks the two invariants a correct PageRank obeys: every
// score is positive and the scores sum to one (within tolerance). A power-law
// graph with hubs makes the sum-to-one check meaningful.
func TestPageRankInvariants(t *testing.T) {
	ds := genDataset(t, gen.Config{Kind: "er", N: 200, P: 0.02, Seed: 5})
	g, err := LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	scores := g.PageRank(0.85, 1e-12, 100)
	if len(scores) != g.NodeCount() {
		t.Fatalf("len(scores) = %d, want %d", len(scores), g.NodeCount())
	}
	var sum float64
	for _, s := range scores {
		if s.Val <= 0 {
			t.Errorf("node %s has non-positive PageRank %g", s.ID, s.Val)
		}
		sum += s.Val
	}
	if math.Abs(sum-1.0) > 1e-6 {
		t.Errorf("PageRank sum = %g, want 1.0", sum)
	}
}

// TestWCCMatchesUnionFind cross-checks the weakly connected components against an
// independent union-find over the edge list read straight from the CSV, including
// the smallest-member labeling.
func TestWCCMatchesUnionFind(t *testing.T) {
	ds := genDataset(t, gen.Config{Kind: "er", N: 120, P: 0.01, Seed: 9})
	g, err := LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	edges := readEdges(t, ds)

	// Union-find over every node id and undirected edge.
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if parent[x] == "" {
			parent[x] = x
		}
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b string) { parent[find(a)] = find(b) }
	for i := 0; i < g.NodeCount(); i++ {
		find(strconv.Itoa(i))
	}
	for _, e := range edges {
		union(e[0], e[1])
	}
	// Smallest numeric member per root.
	wantLabel := map[string]int64{}
	for i := 0; i < g.NodeCount(); i++ {
		id := strconv.Itoa(i)
		root := find(id)
		v := int64(i)
		if cur, ok := wantLabel[root]; !ok || v < cur {
			wantLabel[root] = v
		}
	}

	got := g.WeaklyConnectedComponents()
	if len(got) != g.NodeCount() {
		t.Fatalf("len(WCC) = %d, want %d", len(got), g.NodeCount())
	}
	for _, nl := range got {
		want := wantLabel[find(nl.ID)]
		if nl.Label != want {
			t.Errorf("WCC label of %s = %d, want %d", nl.ID, nl.Label, want)
		}
	}
}

// TestLCCMatchesBruteForce cross-checks the local clustering coefficient against an
// independent recomputation from the edge list, so the coefficient is validated on
// a graph that actually has closed neighbor triples.
func TestLCCMatchesBruteForce(t *testing.T) {
	ds := genDataset(t, gen.Config{Kind: "er", N: 80, P: 0.1, Seed: 11})
	g, err := LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	edges := readEdges(t, ds)

	out := map[string]map[string]struct{}{}
	in := map[string]map[string]struct{}{}
	add := func(m map[string]map[string]struct{}, a, b string) {
		if m[a] == nil {
			m[a] = map[string]struct{}{}
		}
		m[a][b] = struct{}{}
	}
	hasEdge := map[[2]string]struct{}{}
	for _, e := range edges {
		add(out, e[0], e[1])
		add(in, e[1], e[0])
		hasEdge[[2]string{e[0], e[1]}] = struct{}{}
	}
	want := map[string]float64{}
	for i := 0; i < g.NodeCount(); i++ {
		v := strconv.Itoa(i)
		nbrs := map[string]struct{}{}
		for u := range out[v] {
			if u != v {
				nbrs[u] = struct{}{}
			}
		}
		for u := range in[v] {
			if u != v {
				nbrs[u] = struct{}{}
			}
		}
		d := len(nbrs)
		if d < 2 {
			want[v] = 0
			continue
		}
		var links int64
		for u := range nbrs {
			for w := range nbrs {
				if u == w {
					continue
				}
				if _, ok := hasEdge[[2]string{u, w}]; ok {
					links++
				}
			}
		}
		want[v] = float64(links) / (float64(d) * float64(d-1))
	}

	got := g.LocalClustering()
	var checkedNonZero bool
	for _, nf := range got {
		if math.Abs(nf.Val-want[nf.ID]) > 1e-12 {
			t.Errorf("LCC of %s = %g, want %g", nf.ID, nf.Val, want[nf.ID])
		}
		if nf.Val > 0 {
			checkedNonZero = true
		}
	}
	if !checkedNonZero {
		t.Fatal("ER graph produced no clustering; raise N or P for a real cross-check")
	}
}

// TestLabelPropagationDeterministic checks CDLP is reproducible (the determinism
// the spec requires) and that every node carries a label.
func TestLabelPropagationDeterministic(t *testing.T) {
	ds := genDataset(t, gen.Config{Kind: "er", N: 60, P: 0.05, Seed: 13})
	g, err := LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	a := g.LabelPropagation(10)
	b := g.LabelPropagation(10)
	if len(a) != g.NodeCount() {
		t.Fatalf("len(CDLP) = %d, want %d", len(a), g.NodeCount())
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("CDLP not deterministic at row %d: %v vs %v", i, a[i], b[i])
		}
	}
}
