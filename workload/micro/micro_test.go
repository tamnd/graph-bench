package micro_test

import (
	"context"
	"testing"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
	_ "github.com/tamnd/graph-bench/workload/micro" // register the micro workloads
)

// gridDS generates a 5x5 4-neighbor grid in a temp directory and opens it.
// A 5x5 grid has 25 nodes and 40 directed edges (right and down only). The
// corner-to-corner Manhattan distance is 8, and there are zero triangles.
func gridDS(t *testing.T) *dataset.Set {
	t.Helper()
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, err := gen.Generate(context.Background(), gen.Config{Kind: "grid", Rows: 5, Cols: 5}, w); err != nil {
		t.Fatalf("Generate grid: %v", err)
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ds
}

// erDS generates a small ER graph with enough density to have many triangles.
// N=30, P=0.15: the closed-form expected triangle count is N*(N-1)*(N-2)*P^3/6 ≈ 14.
func erDS(t *testing.T) *dataset.Set {
	t.Helper()
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, err := gen.Generate(context.Background(), gen.Config{Kind: "er", N: 30, P: 0.15, Seed: 42}, w); err != nil {
		t.Fatalf("Generate er: %v", err)
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ds
}

// mustLookup retrieves a registered workload or fails immediately.
func mustLookup(t *testing.T, name string) *workload.Workload {
	t.Helper()
	w, ok := workload.Lookup(name)
	if !ok {
		t.Fatalf("workload %q not in registry", name)
	}
	return w
}

// mustQuery retrieves a query from a workload or fails immediately.
func mustQuery(t *testing.T, w *workload.Workload, id string) *workload.WorkloadQuery {
	t.Helper()
	q, ok := w.Query(id)
	if !ok {
		t.Fatalf("workload %q has no query %q", w.Name, id)
	}
	return q
}

// assertScalar fails the test if the answer is not a single row with the given value.
func assertScalar(t *testing.T, ans *target.Answer, want target.Value) {
	t.Helper()
	if len(ans.Rows) != 1 || len(ans.Rows[0]) != 1 {
		t.Fatalf("answer shape: %d rows, want 1x1", len(ans.Rows))
	}
	if ans.Rows[0][0] != want {
		t.Errorf("scalar = %v (%T), want %v (%T)", ans.Rows[0][0], ans.Rows[0][0], want, want)
	}
}

// TestMicroGridRegistered proves the micro-grid workload is in the registry after
// the blank import, with the expected query ids.
func TestMicroGridRegistered(t *testing.T) {
	w := mustLookup(t, "micro-grid")
	if w.Dataset != "grid" {
		t.Errorf("micro-grid.Dataset = %q, want %q", w.Dataset, "grid")
	}
	ids := make([]string, len(w.Queries))
	for i, q := range w.Queries {
		ids[i] = q.ID
	}
	want := []string{"micro-khop1", "micro-khop2", "micro-khop3", "micro-varlen", "micro-sp"}
	for i, id := range want {
		if i >= len(ids) || ids[i] != id {
			t.Errorf("query[%d] = %q, want %q (all: %v)", i, ids[i], id, ids)
		}
	}
}

// TestMicroERRegistered proves the micro-er workload is in the registry with the
// two triangle queries.
func TestMicroERRegistered(t *testing.T) {
	w := mustLookup(t, "micro-er")
	if len(w.Queries) != 2 {
		t.Errorf("micro-er has %d queries, want 2", len(w.Queries))
	}
}

// TestCurateWritesGridPools runs curation on a generated grid dataset and reads
// the resulting parameter pools back through Dataset.Params, checking that the
// khop, sp, and triangle pools are present and correctly shaped.
func TestCurateWritesGridPools(t *testing.T) {
	ds := gridDS(t)

	if err := workload.Curate(ds, 77); err != nil {
		t.Fatalf("Curate: %v", err)
	}

	pool, err := ds.Params("micro-khop")
	if err != nil {
		t.Fatalf("Params(micro-khop): %v", err)
	}
	if len(pool) == 0 {
		t.Fatal("micro-khop pool is empty after curation")
	}
	for i, p := range pool {
		if _, ok := p["seed"].(string); !ok {
			t.Errorf("pool[%d] missing string seed: %v", i, p)
		}
	}

	spPool, err := ds.Params("micro-sp")
	if err != nil {
		t.Fatalf("Params(micro-sp): %v", err)
	}
	if len(spPool) == 0 {
		t.Fatal("micro-sp pool is empty after curation")
	}
	for i, p := range spPool {
		if _, ok := p["src"].(string); !ok {
			t.Errorf("spPool[%d] missing string src: %v", i, p)
		}
		if _, ok := p["dst"].(string); !ok {
			t.Errorf("spPool[%d] missing string dst: %v", i, p)
		}
	}

	triPool, err := ds.Params("micro-triangle")
	if err != nil {
		t.Fatalf("Params(micro-triangle): %v", err)
	}
	if len(triPool) != 1 {
		t.Errorf("micro-triangle pool len = %d, want 1 (empty sentinel)", len(triPool))
	}
}

// TestKHop1GridCorner checks the khop1 reference on the 5x5 grid corner
// (top-left node id "0") and the dead-end corner (bottom-right id "24").
func TestKHop1GridCorner(t *testing.T) {
	ds := gridDS(t)
	q := mustQuery(t, mustLookup(t, "micro-grid"), "micro-khop1")

	// Top-left corner: one right (id 1) and one down (id 5) neighbor. Degree = 2.
	ref, err := q.Reference.Compute(ds, target.Params{"seed": "0"})
	if err != nil {
		t.Fatalf("Compute(0): %v", err)
	}
	assertScalar(t, ref, int64(2))

	// Bottom-right corner: no outgoing edges in the right/down DAG. Degree = 0.
	ref2, err := q.Reference.Compute(ds, target.Params{"seed": "24"})
	if err != nil {
		t.Fatalf("Compute(24): %v", err)
	}
	assertScalar(t, ref2, int64(0))
}

// TestKHop2GridCorner checks the two-hop expansion from node "0" in the 5x5
// grid. One hop reaches {1,5}; two hops from those reach {2,6,10} (id 6 is
// shared). Distinct count = 3.
func TestKHop2GridCorner(t *testing.T) {
	ds := gridDS(t)
	q := mustQuery(t, mustLookup(t, "micro-grid"), "micro-khop2")
	ref, err := q.Reference.Compute(ds, target.Params{"seed": "0"})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	assertScalar(t, ref, int64(3))
}

// TestVarlen1to3GridCorner checks the 1-to-3-hop range from node "0". The
// union over hops 1/2/3 from a 5x5 grid corner: hop1={1,5}, hop2={2,6,10},
// hop3={3,7,11,15}. Union = {1,2,3,5,6,7,10,11,15} = 9 nodes.
func TestVarlen1to3GridCorner(t *testing.T) {
	ds := gridDS(t)
	q := mustQuery(t, mustLookup(t, "micro-grid"), "micro-varlen")
	ref, err := q.Reference.Compute(ds, target.Params{"seed": "0"})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	assertScalar(t, ref, int64(9))
}

// TestSPGridCornerToCorner checks that the single-pair SP reference from
// top-left ("0") to bottom-right ("24") of the 5x5 grid returns the Manhattan
// distance 8.
func TestSPGridCornerToCorner(t *testing.T) {
	ds := gridDS(t)
	q := mustQuery(t, mustLookup(t, "micro-grid"), "micro-sp")
	ref, err := q.Reference.Compute(ds, target.Params{"src": "0", "dst": "24"})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	assertScalar(t, ref, int64(8))
}

// TestSPGridUnreachable checks that the reverse pair ("24" to "0") in the
// right/down DAG returns no row.
func TestSPGridUnreachable(t *testing.T) {
	ds := gridDS(t)
	q := mustQuery(t, mustLookup(t, "micro-grid"), "micro-sp")
	ref, err := q.Reference.Compute(ds, target.Params{"src": "24", "dst": "0"})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(ref.Rows) != 0 {
		t.Errorf("reverse pair has %d rows, want 0 (unreachable)", len(ref.Rows))
	}
}

// TestTriangleGridIsZero proves the directed and undirected triangle counts on
// the 4-neighbor grid (a DAG and a bipartite graph) are zero, matching the
// generator's TriangleCount=0 invariant.
func TestTriangleGridIsZero(t *testing.T) {
	ds := gridDS(t)
	g, err := workload.LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	if c := g.DirectedTriangles(); c != 0 {
		t.Errorf("DirectedTriangles on grid = %d, want 0", c)
	}
	if c := g.UndirectedTriangles(); c != 0 {
		t.Errorf("UndirectedTriangles on grid = %d, want 0", c)
	}

	// The generator records this as an invariant; cross-check.
	inv := ds.Manifest().Invariants.TriangleCount
	if inv != nil && *inv != 0 {
		t.Errorf("manifest TriangleCount = %d, want 0", *inv)
	}
}

// TestTriangleERPositive checks that the directed triangle reference on the ER
// graph returns a positive count (proving the oracle works on a graph with actual
// triangles) and matches the oracle independently.
func TestTriangleERPositive(t *testing.T) {
	ds := erDS(t)
	q := mustQuery(t, mustLookup(t, "micro-er"), "micro-triangle")
	ref, err := q.Reference.Compute(ds, nil)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(ref.Rows) != 1 || len(ref.Rows[0]) != 1 {
		t.Fatalf("unexpected answer shape: %v", ref.Rows)
	}
	n, ok := ref.Rows[0][0].(int64)
	if !ok {
		t.Fatalf("triangle count is %T, want int64", ref.Rows[0][0])
	}
	if n <= 0 {
		t.Errorf("triangle count = %d, want > 0 on N=30 P=0.15 ER", n)
	}

	g, err := workload.LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	if got := g.DirectedTriangles(); got != n {
		t.Errorf("oracle DirectedTriangles = %d, reference returned %d", got, n)
	}
}

// TestResolveDialectCarriesReference checks that Resolve for Cypher picks the
// query text and carries the reference answer through unchanged.
func TestResolveDialectCarriesReference(t *testing.T) {
	w := mustLookup(t, "micro-grid")
	q := mustQuery(t, w, "micro-khop1")

	ref := &target.Answer{Columns: []string{"n"}, Rows: [][]target.Value{{int64(3)}}}
	tq, params, ok := q.Resolve(workload.Cypher, ref)
	if !ok {
		t.Fatal("Resolve(Cypher) not ok")
	}
	if tq.ID != "micro-khop1" || tq.Class != target.Traversal {
		t.Errorf("resolved id/class = %q/%v", tq.ID, tq.Class)
	}
	if tq.Reference != ref {
		t.Error("reference was not carried through Resolve")
	}
	if params != nil {
		t.Errorf("params = %v, want nil (query has no ParamSource)", params)
	}

	// A dialect with no text is a blank cell, not a failure.
	if _, _, ok := q.Resolve(workload.AGE, nil); ok {
		t.Error("Resolve(AGE) ok, want blank cell (no AGE text)")
	}
}
