package workload

import (
	"math"
	"strconv"
	"testing"
	"time"
)

// The analytics oracles are tested against small hand-computed graphs built
// directly in memory (the package owns the Graph representation, so tests can
// assemble fixtures without the CSV plumbing oracle_test.go already covers).

// wedge is one directed weighted test edge; a zero weight means 1, matching a
// dataset without a "w" column.
type wedge struct {
	from, to int
	w        int64
}

// mkGraph builds a Graph with node ids "0".."n-1" and the given edges.
func mkGraph(n int, edges []wedge) *Graph {
	g := &Graph{index: map[string]int{}}
	for i := 0; i < n; i++ {
		g.intern(strconv.Itoa(i))
	}
	for _, e := range edges {
		s := g.index[strconv.Itoa(e.from)]
		d := g.index[strconv.Itoa(e.to)]
		w := e.w
		if w == 0 {
			w = 1
		}
		g.out[s] = append(g.out[s], d)
		g.outW[s] = append(g.outW[s], w)
		g.in[d] = append(g.in[d], s)
	}
	g.sortAdjacency()
	return g
}

// mkGraphFromPairs builds a unit-weight Graph from a string edge list (the
// erEdges fixtures shared with oracle_test.go).
func mkGraphFromPairs(n int, pairs [][2]string) *Graph {
	edges := make([]wedge, len(pairs))
	for i, p := range pairs {
		f, _ := strconv.Atoi(p[0])
		t, _ := strconv.Atoi(p[1])
		edges[i] = wedge{from: f, to: t}
	}
	return mkGraph(n, edges)
}

// gridWedges is the 3x3 right/down lattice as in-memory edges: id(r,c)=r*3+c.
func gridWedges() []wedge {
	var edges []wedge
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			if c+1 < 3 {
				edges = append(edges, wedge{from: r*3 + c, to: r*3 + c + 1})
			}
			if r+1 < 3 {
				edges = append(edges, wedge{from: r*3 + c, to: (r+1)*3 + c})
			}
		}
	}
	return edges
}

// TestBFSLevelsGrid checks BFS levels against the grid's closed form. On a
// 3x3 right/down DAG the level of node id = r*3+c from node 0 is the
// Manhattan distance r+c, and every node is reachable.
func TestBFSLevelsGrid(t *testing.T) {
	g := mkGraph(9, gridWedges())
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
	if _, ok := g.BFSLevels("99"); ok {
		t.Error("BFSLevels(99) ok for an unknown source, want false")
	}
}

// TestEdgesReachedGrid checks the TEPS edge count: from corner 0 every node
// is reached, so every one of the 12 grid edges is examined.
func TestEdgesReachedGrid(t *testing.T) {
	g := mkGraph(9, gridWedges())
	edges, ok := g.EdgesReached("0")
	if !ok || edges != 12 {
		t.Errorf("EdgesReached(0) = %d,%v, want 12,true", edges, ok)
	}
	// From the sink corner nothing leaves.
	edges, ok = g.EdgesReached("8")
	if !ok || edges != 0 {
		t.Errorf("EdgesReached(8) = %d,%v, want 0,true", edges, ok)
	}
}

// TestSSSPUnitMatchesBFS checks the unit-weight SSSP equals the BFS level in
// float form, the relationship the oracle relies on for the unweighted
// synthetic graphs.
func TestSSSPUnitMatchesBFS(t *testing.T) {
	g := mkGraphFromPairs(50, erEdges(50, 0.08, 3))
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

// TestSSSPWeightedHandComputed checks Dijkstra on a hand-computed fixture
// where the cheapest route is not the fewest hops: 0->1 costs 5 directly but
// 2 via node 2. Node 3 is isolated and must be omitted.
func TestSSSPWeightedHandComputed(t *testing.T) {
	g := mkGraph(4, []wedge{
		{from: 0, to: 1, w: 5},
		{from: 0, to: 2, w: 1},
		{from: 2, to: 1, w: 1},
	})
	dists, ok := g.SSSPWeighted("0")
	if !ok {
		t.Fatal("SSSPWeighted(0) not ok")
	}
	want := []NodeFloat{{ID: "0", Val: 0}, {ID: "1", Val: 2}, {ID: "2", Val: 1}}
	if len(dists) != len(want) {
		t.Fatalf("dists = %v, want %v", dists, want)
	}
	for i := range want {
		if dists[i] != want[i] {
			t.Errorf("dists[%d] = %v, want %v", i, dists[i], want[i])
		}
	}
	if _, ok := g.SSSPWeighted("99"); ok {
		t.Error("SSSPWeighted(99) ok for an unknown source, want false")
	}
}

// TestSSSPWeightedUnitMatchesBFS checks Dijkstra reduces to BFS levels when
// every weight is one, cross-validating the heap implementation against the
// plain frontier walk on a branching graph.
func TestSSSPWeightedUnitMatchesBFS(t *testing.T) {
	g := mkGraphFromPairs(50, erEdges(50, 0.08, 3))
	levels, _ := g.BFSLevels("0")
	dists, _ := g.SSSPWeighted("0")
	if len(levels) != len(dists) {
		t.Fatalf("len mismatch: bfs %d, dijkstra %d", len(levels), len(dists))
	}
	for i := range levels {
		if levels[i].ID != dists[i].ID || float64(levels[i].Val) != dists[i].Val {
			t.Errorf("row %d: bfs %v, dijkstra %v", i, levels[i], dists[i])
		}
	}
}

// TestPageRankInvariants checks the two invariants a correct PageRank obeys:
// every score is positive and the scores sum to one (within tolerance).
func TestPageRankInvariants(t *testing.T) {
	g := mkGraphFromPairs(200, erEdges(200, 0.02, 5))
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

// TestWCCMatchesUnionFind cross-checks the weakly connected components
// against an independent union-find over the same edge list, including the
// smallest-member labeling.
func TestWCCMatchesUnionFind(t *testing.T) {
	edges := erEdges(120, 0.01, 9)
	g := mkGraphFromPairs(120, edges)

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
	wantLabel := map[string]int64{}
	for i := 0; i < g.NodeCount(); i++ {
		root := find(strconv.Itoa(i))
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

// TestLCCMatchesBruteForce cross-checks the local clustering coefficient
// against an independent recomputation from the edge list, so the
// coefficient is validated on a graph that actually has closed neighbor
// triples.
func TestLCCMatchesBruteForce(t *testing.T) {
	edges := erEdges(80, 0.1, 11)
	g := mkGraphFromPairs(80, edges)

	hasEdge := map[[2]string]struct{}{}
	out := map[string]map[string]struct{}{}
	in := map[string]map[string]struct{}{}
	add := func(m map[string]map[string]struct{}, a, b string) {
		if m[a] == nil {
			m[a] = map[string]struct{}{}
		}
		m[a][b] = struct{}{}
	}
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

// TestLabelPropagationDeterministic checks CDLP is reproducible (the
// determinism the spec requires) and that every node carries a label.
func TestLabelPropagationDeterministic(t *testing.T) {
	g := mkGraphFromPairs(60, erEdges(60, 0.05, 13))
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

// TestTriangleCountTotal checks the GAP TC oracle on the hand-computed
// 3-cycle (one undirected triangle) and its agreement with the
// UndirectedTriangles it aliases on a graph with many triangles.
func TestTriangleCountTotal(t *testing.T) {
	k3 := mkGraph(3, []wedge{{from: 0, to: 1}, {from: 1, to: 2}, {from: 2, to: 0}})
	if c := k3.TriangleCountTotal(); c != 1 {
		t.Errorf("TriangleCountTotal(3-cycle) = %d, want 1", c)
	}
	g := mkGraphFromPairs(60, erEdges(60, 0.1, 7))
	if got, want := g.TriangleCountTotal(), g.UndirectedTriangles(); got != want || got == 0 {
		t.Errorf("TriangleCountTotal = %d, want %d (non-zero)", got, want)
	}
}

// TestBetweennessExactPath checks Brandes on the hand-computed directed path
// 0->1->2->3 with every node a source: node 1 lies on the 0->2 and 0->3
// pairs (dependency 2), node 2 on 0->3 and 1->3 (dependency 2), the
// endpoints on none.
func TestBetweennessExactPath(t *testing.T) {
	g := mkGraph(4, []wedge{{from: 0, to: 1}, {from: 1, to: 2}, {from: 2, to: 3}})
	got := g.BetweennessExact([]int{0, 1, 2, 3})
	want := []float64{0, 2, 2, 0}
	for i, nf := range got {
		if math.Abs(nf.Val-want[i]) > 1e-12 {
			t.Errorf("BC of %s = %g, want %g", nf.ID, nf.Val, want[i])
		}
	}
}

// TestBetweennessExactDiamond checks the shortest-path multiplicity split:
// on the diamond 0->1->3, 0->2->3 the two middle nodes each carry half of
// the single 0->3 pair dependency from source 0.
func TestBetweennessExactDiamond(t *testing.T) {
	g := mkGraph(4, []wedge{
		{from: 0, to: 1}, {from: 0, to: 2},
		{from: 1, to: 3}, {from: 2, to: 3},
	})
	got := g.BetweennessExact([]int{0})
	want := []float64{0, 0.5, 0.5, 0}
	for i, nf := range got {
		if math.Abs(nf.Val-want[i]) > 1e-12 {
			t.Errorf("BC of %s = %g, want %g", nf.ID, nf.Val, want[i])
		}
	}
	// A source id absent from the graph contributes nothing.
	for _, nf := range g.BetweennessExact([]int{99}) {
		if nf.Val != 0 {
			t.Errorf("BC of %s = %g from an unknown source, want 0", nf.ID, nf.Val)
		}
	}
}

// TestValidateBFSTree exercises the Graph500 K2 parent-tree checks on a
// hand-computed graph: 0->1, 1->2 with the shortcut 0->2 (so 1 and 2 are
// both at level one from 0), a second level via 2->3, and a separate
// component 4->5.
func TestValidateBFSTree(t *testing.T) {
	g := mkGraph(6, []wedge{
		{from: 0, to: 1}, {from: 1, to: 2}, {from: 0, to: 2},
		{from: 2, to: 3}, {from: 4, to: 5},
	})

	valid := map[string]string{"0": "0", "1": "0", "2": "0", "3": "2"}
	if err := g.ValidateBFSTree("0", valid); err != nil {
		t.Errorf("valid tree rejected: %v", err)
	}

	cases := []struct {
		name   string
		src    string
		parent map[string]string
	}{
		{"unknown source", "99", valid},
		{"root missing", "0", map[string]string{"1": "0"}},
		{"root not own parent", "0", map[string]string{"0": "1", "1": "0"}},
		{"unknown node", "0", map[string]string{"0": "0", "9": "0"}},
		{"unknown parent", "0", map[string]string{"0": "0", "1": "9"}},
		{"edge not in graph", "0", map[string]string{"0": "0", "3": "0"}},
		{"levels differ by two, not one", "0", map[string]string{"0": "0", "1": "0", "2": "1"}},
		{"unreachable node in tree", "0", map[string]string{"0": "0", "5": "4"}},
	}
	for _, c := range cases {
		if err := g.ValidateBFSTree(c.src, c.parent); err == nil {
			t.Errorf("%s: ValidateBFSTree returned nil, want an error", c.name)
		}
	}
}

// TestPowerScore checks the geometric mean: geomean(1s, 4s) = 2s, a single
// time is itself, and empty or non-positive inputs score zero.
func TestPowerScore(t *testing.T) {
	if got := PowerScore([]time.Duration{time.Second, 4 * time.Second}); math.Abs(got-2.0) > 1e-12 {
		t.Errorf("PowerScore(1s,4s) = %g, want 2", got)
	}
	if got := PowerScore([]time.Duration{500 * time.Millisecond}); math.Abs(got-0.5) > 1e-12 {
		t.Errorf("PowerScore(500ms) = %g, want 0.5", got)
	}
	if got := PowerScore(nil); got != 0 {
		t.Errorf("PowerScore(nil) = %g, want 0", got)
	}
	if got := PowerScore([]time.Duration{time.Second, 0}); got != 0 {
		t.Errorf("PowerScore with a zero time = %g, want 0", got)
	}
}
