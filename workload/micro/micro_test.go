package micro_test

import (
	"context"
	"testing"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
	_ "github.com/tamnd/graph-bench/workload/micro"
)

// genDS generates a synthetic dataset into a temp dir and opens it.
func genDS(t *testing.T, cfg gen.Config) *dataset.Set {
	t.Helper()
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, err := gen.Generate(context.Background(), cfg, w); err != nil {
		t.Fatalf("Generate %s: %v", cfg.Kind, err)
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ds
}

// ref computes a query's reference answer.
func ref(t *testing.T, wl *workload.Workload, id string, ds *dataset.Set, p workload.Params) *workload.Answer {
	t.Helper()
	q, ok := wl.Query(id)
	if !ok {
		t.Fatalf("workload %s has no query %s", wl.Name, id)
	}
	ans, err := q.Reference.Compute(ds, p)
	if err != nil {
		t.Fatalf("%s reference: %v", id, err)
	}
	return ans
}

// oneCount extracts the single integer a count answer carries.
func oneCount(t *testing.T, id string, ans *workload.Answer) int64 {
	t.Helper()
	if len(ans.Rows) != 1 || len(ans.Rows[0]) < 1 {
		t.Fatalf("%s: want one row, got %v", id, ans.Rows)
	}
	n, ok := ans.Rows[0][0].(int64)
	if !ok {
		t.Fatalf("%s: first cell is %T, want int64", id, ans.Rows[0][0])
	}
	return n
}

// TestRegistration checks the registered workloads carry the v1 query ids, the
// right datasets, and pool keys that workload.Curate serves.
func TestRegistration(t *testing.T) {
	read, err := workload.Lookup("micro-read")
	if err != nil {
		t.Fatalf("Lookup(micro-read): %v", err)
	}
	if read.Dataset != "grid-100x100" || read.Family != "micro" {
		t.Errorf("micro-read dataset/family = %q/%q", read.Dataset, read.Family)
	}
	wantIDs := []string{
		"micro-point", "micro-point-miss", "micro-edge",
		"micro-khop1", "micro-khop2", "micro-khop3", "micro-varlen",
		"micro-scan-count", "micro-scan-stats",
	}
	if len(read.Queries) != len(wantIDs) {
		t.Fatalf("micro-read has %d queries, want %d", len(read.Queries), len(wantIDs))
	}
	for _, id := range wantIDs {
		q, ok := read.Query(id)
		if !ok {
			t.Errorf("micro-read missing query %s", id)
			continue
		}
		if q.Texts[engine.Cypher] == "" {
			t.Errorf("%s has no cypher text", id)
		}
		if q.PoolKey == "" && q.Params == nil {
			t.Errorf("%s has neither a pool key nor fixed params", id)
		}
	}
	// The pooled queries must name pools Curate can actually serve.
	ds := genDS(t, gen.Config{Kind: "grid", Rows: 4, Cols: 4})
	for _, q := range read.Queries {
		if q.PoolKey == "" {
			continue
		}
		if _, err := workload.Curate(ds, q.PoolKey, 4, 1); err != nil {
			t.Errorf("Curate(%s) for query %s: %v", q.PoolKey, q.ID, err)
		}
	}

	er, err := workload.Lookup("micro-er")
	if err != nil {
		t.Fatalf("Lookup(micro-er): %v", err)
	}
	if er.Dataset != "er-10k" || len(er.Queries) != 2 {
		t.Errorf("micro-er dataset %q, %d queries; want er-10k, 2", er.Dataset, len(er.Queries))
	}

	for _, tc := range []struct {
		name, dataset string
	}{
		{"micro-powerlaw", "powerlaw-10k"},
		{"micro-uniform", "uniform-10k"},
	} {
		w, err := workload.Lookup(tc.name)
		if err != nil {
			t.Fatalf("Lookup(%s): %v", tc.name, err)
		}
		if w.Dataset != tc.dataset || w.Family != "micro" {
			t.Errorf("%s dataset/family = %q/%q, want %q/micro", tc.name, w.Dataset, w.Family, tc.dataset)
		}
		if len(w.Queries) == 0 {
			t.Errorf("%s has no queries", tc.name)
		}
	}

	// The two path queries are the only ones that need a shortest-path
	// capability, and they must say so: without the flag they resolve to the
	// plain Cypher text on engines with no shortest-path syntax, which fails
	// verification and discards the whole workload's measurement instead of
	// skipping two queries.
	pl, err := workload.Lookup("micro-powerlaw")
	if err != nil {
		t.Fatalf("Lookup(micro-powerlaw): %v", err)
	}
	for _, id := range []string{"micro-sp", "micro-sp-bidir"} {
		q, ok := pl.Query(id)
		if !ok {
			t.Errorf("micro-powerlaw missing %s", id)
			continue
		}
		if !q.NeedsShortestPath {
			t.Errorf("%s does not set NeedsShortestPath", id)
		}
	}

	mix, err := workload.Lookup("micro-mix")
	if err != nil {
		t.Fatalf("Lookup(micro-mix): %v", err)
	}
	if mix.Mix == nil || len(mix.Mix.Weights) == 0 {
		t.Fatal("micro-mix has no mix weights")
	}
	for id := range mix.Mix.Weights {
		if _, ok := mix.Query(id); !ok {
			t.Errorf("mix weight names unknown query %s", id)
		}
	}
	// And the other direction: a query in the mix with no weight is never
	// scheduled, so it is dead configuration rather than a benchmark.
	for _, q := range mix.Queries {
		if mix.Mix.Weights[q.ID] == 0 {
			t.Errorf("micro-mix carries query %s with no weight", q.ID)
		}
	}
}

// TestGridEndToEnd is the end-to-end path on a small grid: generate, curate
// every pool, and compute references for each binding; then pin the
// closed-form values a 5x5 right/down grid dictates, including the
// non-degeneracy khop2 > khop1 at a mid-grid seed.
func TestGridEndToEnd(t *testing.T) {
	ds := genDS(t, gen.Config{Kind: "grid", Rows: 5, Cols: 5})
	read, err := workload.Lookup("micro-read")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	// Every pooled query's references compute cleanly over its curated pool.
	for _, q := range read.Queries {
		if q.PoolKey == "" {
			// Parameterless: one reference off the fixed (empty) binding.
			if _, err := q.Reference.Compute(ds, q.Params.Next()); err != nil {
				t.Errorf("%s reference: %v", q.ID, err)
			}
			continue
		}
		pool, err := workload.Curate(ds, q.PoolKey, 8, 7)
		if err != nil {
			t.Fatalf("Curate(%s): %v", q.PoolKey, err)
		}
		if len(pool) == 0 {
			t.Fatalf("Curate(%s) returned an empty pool", q.PoolKey)
		}
		for _, p := range pool {
			if _, err := q.Reference.Compute(ds, p); err != nil {
				t.Errorf("%s reference for %v: %v", q.ID, p, err)
			}
		}
	}

	// Node 12 is the center of the 5x5 grid, at (row 2, col 2). Only right and
	// down edges exist, so the nodes at exactly h hops are the cells (2+dr,
	// 2+dc) with dr+dc = h and both offsets within the two rows and two columns
	// that remain: 2 at one hop, 3 at two, and back down to 2 at three, where
	// the grid boundary starts clipping the frontier. The 1..3 union is the sum.
	k1 := oneCount(t, "micro-khop1", ref(t, read, "micro-khop1", ds, workload.Params{"seed": "12"}))
	k2 := oneCount(t, "micro-khop2", ref(t, read, "micro-khop2", ds, workload.Params{"seed": "12"}))
	k3 := oneCount(t, "micro-khop3", ref(t, read, "micro-khop3", ds, workload.Params{"seed": "12"}))
	vl := oneCount(t, "micro-varlen", ref(t, read, "micro-varlen", ds, workload.Params{"seed": "12"}))
	if k1 != 2 || k2 != 3 || k3 != 2 {
		t.Errorf("khop1/2/3 at seed 12 = %d/%d/%d, want 2/3/2", k1, k2, k3)
	}
	if vl != k1+k2+k3 {
		t.Errorf("varlen at seed 12 = %d, want %d (the three shells are disjoint on a DAG grid)", vl, k1+k2+k3)
	}
	if k2 <= k1 {
		t.Errorf("khop2 (%d) should exceed khop1 (%d) at a mid-grid seed", k2, k1)
	}
	// The bottom-right corner is a sink.
	if n := oneCount(t, "micro-khop1", ref(t, read, "micro-khop1", ds, workload.Params{"seed": "24"})); n != 0 {
		t.Errorf("khop1 at sink 24 = %d, want 0", n)
	}

	// Point hit and miss.
	hit := ref(t, read, "micro-point", ds, workload.Params{"id": "7"})
	if len(hit.Rows) != 1 || hit.Rows[0][0] != int64(7) {
		t.Errorf("micro-point(7) = %v, want [[7]]", hit.Rows)
	}
	miss := ref(t, read, "micro-point-miss", ds, workload.Params{"id": "9999"})
	if len(miss.Rows) != 0 {
		t.Errorf("micro-point-miss(9999) = %v, want no rows", miss.Rows)
	}
	q, _ := read.Query("micro-point-miss")
	if _, err := q.Reference.Compute(ds, workload.Params{"id": "3"}); err == nil {
		t.Error("micro-point-miss with a present id should error (corrupt pool)")
	}

	// Edge probe: 0->1 exists (right neighbor), the reverse does not.
	if ans := ref(t, read, "micro-edge", ds, workload.Params{"src": "0", "dst": "1"}); ans.Rows[0][0] != true {
		t.Errorf("micro-edge(0,1) = %v, want true", ans.Rows[0][0])
	}
	if ans := ref(t, read, "micro-edge", ds, workload.Params{"src": "1", "dst": "0"}); ans.Rows[0][0] != false {
		t.Errorf("micro-edge(1,0) = %v, want false", ans.Rows[0][0])
	}

	// Scans: 25 nodes, ids 0..24, mean 12.
	if n := oneCount(t, "micro-scan-count", ref(t, read, "micro-scan-count", ds, nil)); n != 25 {
		t.Errorf("micro-scan-count = %d, want 25", n)
	}
	stats := ref(t, read, "micro-scan-stats", ds, nil)
	if stats.Rows[0][0] != int64(25) || stats.Rows[0][1] != 12.0 {
		t.Errorf("micro-scan-stats = %v, want [25 12]", stats.Rows[0])
	}
}

// TestShortestPathRefs pins the two path references on the same 5x5 grid,
// where every distance is Manhattan and every unreachable pair is obvious.
// The grid is a right/down DAG, so directed reachability runs one way only and
// the undirected variant answers where the directed one has no row at all,
// which is the whole difference between the two queries.
func TestShortestPathRefs(t *testing.T) {
	ds := genDS(t, gen.Config{Kind: "grid", Rows: 5, Cols: 5})
	pl, err := workload.Lookup("micro-powerlaw")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	// 12 is (2,2) and 24 is (4,4): four right/down steps.
	if d := oneCount(t, "micro-sp", ref(t, pl, "micro-sp", ds, workload.Params{"src": "12", "dst": "24"})); d != 4 {
		t.Errorf("micro-sp(12,24) = %d, want 4", d)
	}
	// Backwards there is no directed path, and no row is the answer.
	if ans := ref(t, pl, "micro-sp", ds, workload.Params{"src": "24", "dst": "12"}); len(ans.Rows) != 0 {
		t.Errorf("micro-sp(24,12) = %v, want no rows on a right/down DAG", ans.Rows)
	}
	// Undirected, the same pair is four hops in either direction.
	for _, p := range []workload.Params{{"src": "12", "dst": "24"}, {"src": "24", "dst": "12"}} {
		if d := oneCount(t, "micro-sp-bidir", ref(t, pl, "micro-sp-bidir", ds, p)); d != 4 {
			t.Errorf("micro-sp-bidir(%v) = %d, want 4", p, d)
		}
	}

	// The sp pool must be one Curate serves, and every pair it draws must have
	// a reference that computes.
	pool, err := workload.Curate(ds, "micro-sp", 8, 7)
	if err != nil {
		t.Fatalf("Curate(micro-sp): %v", err)
	}
	if len(pool) == 0 {
		t.Fatal("Curate(micro-sp) returned an empty pool")
	}
	q, _ := pl.Query("micro-sp")
	for _, p := range pool {
		if _, err := q.Reference.Compute(ds, p); err != nil {
			t.Errorf("micro-sp reference for %v: %v", p, err)
		}
	}
}

// TestERTriangles checks the triangle references on a small ER draw: the
// directed count is exactly three times the oracle's distinct directed
// triangles, non-zero at this density, and zero on the (acyclic) grid.
func TestERTriangles(t *testing.T) {
	ds := genDS(t, gen.Config{Kind: "er", N: 30, P: 0.15, Seed: 42})
	er, err := workload.Lookup("micro-er")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	g, err := workload.LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}

	dir := oneCount(t, "micro-triangle", ref(t, er, "micro-triangle", ds, nil))
	if want := 3 * g.DirectedTriangles(); dir != want {
		t.Errorf("micro-triangle = %d, want 3*%d", dir, g.DirectedTriangles())
	}
	if dir == 0 {
		t.Error("micro-triangle = 0 on ER(30, 0.15); degenerate fixture")
	}
	und := oneCount(t, "micro-triangle-undirected", ref(t, er, "micro-triangle-undirected", ds, nil))
	if und < dir/3 {
		t.Errorf("undirected triangles %d < distinct directed %d; impossible", und, dir/3)
	}

	// The right/down grid is a DAG: no directed triangles, and 4-cycles only,
	// so no undirected triangles either.
	grid := genDS(t, gen.Config{Kind: "grid", Rows: 4, Cols: 4})
	if n := oneCount(t, "micro-triangle", ref(t, er, "micro-triangle", grid, nil)); n != 0 {
		t.Errorf("micro-triangle on grid = %d, want 0", n)
	}
	if n := oneCount(t, "micro-triangle-undirected", ref(t, er, "micro-triangle-undirected", grid, nil)); n != 0 {
		t.Errorf("micro-triangle-undirected on grid = %d, want 0", n)
	}
}
