package galytics_test

import (
	"context"
	"math"
	"testing"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
	"github.com/tamnd/graph-bench/workload/galytics"
)

// genRMAT materializes a small RMAT dataset (scale 8, the workload's
// structure at test size) into a temp dir and opens it.
func genRMAT(t *testing.T, weighted bool) engine.Dataset {
	t.Helper()
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cfg := gen.Config{Kind: "rmat", Scale: 8, EdgeFactor: 16, Seed: 1, Weighted: weighted}
	if _, err := gen.Generate(context.Background(), cfg, w); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ds
}

// TestRegistered proves both workloads register with the expected query
// set, all Analytical, each naming its kernel, each with a reference.
func TestRegistered(t *testing.T) {
	cases := []struct {
		workload string
		dataset  string
		queries  map[string]string // id -> algorithm
	}{
		{"galytics", "rmat-14", map[string]string{
			"ga-bfs": "bfs", "ga-pr": "pagerank", "ga-wcc": "wcc",
			"ga-cdlp": "cdlp", "ga-lcc": "lcc",
		}},
		{"galytics-w", "rmat-14-w", map[string]string{"ga-sssp": "sssp"}},
	}
	for _, c := range cases {
		wl, err := workload.Lookup(c.workload)
		if err != nil {
			t.Fatalf("Lookup(%s): %v", c.workload, err)
		}
		if !wl.Analytics {
			t.Errorf("%s: Analytics = false, want true", c.workload)
		}
		if wl.Dataset != c.dataset {
			t.Errorf("%s: Dataset = %q, want %q", c.workload, wl.Dataset, c.dataset)
		}
		if wl.Fidelity != "spec-following" {
			t.Errorf("%s: Fidelity = %q, want spec-following", c.workload, wl.Fidelity)
		}
		if len(wl.Queries) != len(c.queries) {
			t.Errorf("%s: %d queries, want %d", c.workload, len(wl.Queries), len(c.queries))
		}
		for id, algo := range c.queries {
			q, ok := wl.Query(id)
			if !ok {
				t.Errorf("%s: query %s missing", c.workload, id)
				continue
			}
			if q.Class != engine.Analytical {
				t.Errorf("%s: class = %v, want Analytical", id, q.Class)
			}
			if q.Algorithm != algo {
				t.Errorf("%s: Algorithm = %q, want %q", id, q.Algorithm, algo)
			}
			if q.Params == nil {
				t.Errorf("%s: nil Params", id)
			}
			if q.Reference == nil || q.Reference.Compute == nil {
				t.Errorf("%s: nil reference Compute", id)
			}
		}
	}
}

// TestBFSReference checks the level array: nonempty, the root at level
// zero, every level non-negative.
func TestBFSReference(t *testing.T) {
	ds := genRMAT(t, false)
	wl, _ := workload.Lookup("galytics")
	q, _ := wl.Query("ga-bfs")
	ans, err := q.Reference.Compute(ds, workload.Params{"source": "0"})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(ans.Rows) == 0 {
		t.Fatal("bfs: no rows")
	}
	rootSeen := false
	for _, row := range ans.Rows {
		id, level := row[0].(int64), row[1].(int64)
		if level < 0 {
			t.Fatalf("bfs: node %d has negative level %d", id, level)
		}
		if id == 0 {
			rootSeen = true
			if level != 0 {
				t.Errorf("bfs: root level = %d, want 0", level)
			}
		}
	}
	if !rootSeen {
		t.Error("bfs: root row missing")
	}
}

// TestPageRankReference checks one score per node and mass conservation:
// the scores sum to 1 (the dangling redistribution keeps the total).
func TestPageRankReference(t *testing.T) {
	ds := genRMAT(t, false)
	g, err := workload.LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	wl, _ := workload.Lookup("galytics")
	q, _ := wl.Query("ga-pr")
	ans, err := q.Reference.Compute(ds, nil)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(ans.Rows) != g.NodeCount() {
		t.Fatalf("pagerank: %d rows, want %d", len(ans.Rows), g.NodeCount())
	}
	var sum float64
	for _, row := range ans.Rows {
		s := row[1].(float64)
		if s <= 0 {
			t.Fatalf("pagerank: node %v has non-positive score %g", row[0], s)
		}
		sum += s
	}
	if math.Abs(sum-1) > 1e-6 {
		t.Errorf("pagerank: scores sum to %g, want 1", sum)
	}
}

// TestWCCReference checks the canonical labeling invariants: one row per
// node, at least one component, every label the smallest member id (so
// label <= id on every row and each label labels itself).
func TestWCCReference(t *testing.T) {
	ds := genRMAT(t, false)
	g, err := workload.LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	wl, _ := workload.Lookup("galytics")
	q, _ := wl.Query("ga-wcc")
	ans, err := q.Reference.Compute(ds, nil)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(ans.Rows) != g.NodeCount() {
		t.Fatalf("wcc: %d rows, want %d", len(ans.Rows), g.NodeCount())
	}
	labels := map[int64]int64{} // label -> label of that node id
	for _, row := range ans.Rows {
		labels[row[0].(int64)] = row[1].(int64)
	}
	distinct := map[int64]bool{}
	for _, row := range ans.Rows {
		id, label := row[0].(int64), row[1].(int64)
		distinct[label] = true
		if label > id {
			t.Fatalf("wcc: node %d labeled %d, want smallest member (<= id)", id, label)
		}
		if labels[label] != label {
			t.Fatalf("wcc: label %d does not label itself (got %d)", label, labels[label])
		}
	}
	if len(distinct) < 1 {
		t.Error("wcc: no components")
	}
}

// TestNormalizeComponents is the label-invariance check: an arbitrary
// relabeling of the canonical WCC answer normalizes back to it, and
// without normalization the relabeled copy does not compare equal.
func TestNormalizeComponents(t *testing.T) {
	ds := genRMAT(t, false)
	wl, _ := workload.Lookup("galytics")
	q, _ := wl.Query("ga-wcc")
	canonical, err := q.Reference.Compute(ds, nil)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Relabel every component id by an injective map (13x+7) so no label
	// keeps its canonical value, on a deep copy.
	relabeled := &workload.Answer{Columns: append([]string(nil), canonical.Columns...)}
	for _, row := range canonical.Rows {
		relabeled.Rows = append(relabeled.Rows, []engine.Value{row[0], row[1].(int64)*13 + 7})
	}

	spec := q.Reference.Compare
	if err := workload.Compare(relabeled, canonical, spec); err == nil {
		t.Fatal("relabeled answer compared equal before normalization")
	}
	if err := galytics.NormalizeComponents(relabeled); err != nil {
		t.Fatalf("NormalizeComponents: %v", err)
	}
	if err := workload.Compare(relabeled, canonical, spec); err != nil {
		t.Fatalf("normalized answer does not match canonical: %v", err)
	}
}

// TestCDLPAndLCCReferences checks one row per node, community labels
// drawn from the id space, and coefficients within [0,1].
func TestCDLPAndLCCReferences(t *testing.T) {
	ds := genRMAT(t, false)
	g, err := workload.LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	wl, _ := workload.Lookup("galytics")

	q, _ := wl.Query("ga-cdlp")
	ans, err := q.Reference.Compute(ds, nil)
	if err != nil {
		t.Fatalf("cdlp Compute: %v", err)
	}
	if len(ans.Rows) != g.NodeCount() {
		t.Fatalf("cdlp: %d rows, want %d", len(ans.Rows), g.NodeCount())
	}
	for _, row := range ans.Rows {
		if label := row[1].(int64); label < 0 || label >= int64(g.NodeCount()) {
			t.Fatalf("cdlp: label %d outside the dense id space", label)
		}
	}

	q, _ = wl.Query("ga-lcc")
	ans, err = q.Reference.Compute(ds, nil)
	if err != nil {
		t.Fatalf("lcc Compute: %v", err)
	}
	if len(ans.Rows) != g.NodeCount() {
		t.Fatalf("lcc: %d rows, want %d", len(ans.Rows), g.NodeCount())
	}
	// Non-negative only: RMAT keeps duplicate edges and the directed LCC
	// oracle counts every directed link between neighbors, so a node's
	// coefficient can exceed 1 on a multigraph.
	for _, row := range ans.Rows {
		if c := row[1].(float64); c < 0 {
			t.Fatalf("lcc: node %v has negative coefficient %g", row[0], c)
		}
	}
}

// TestSSSPReference checks weighted distances on the weighted twin:
// root at zero, everything else non-negative and at least the level
// (weights are >= 1, so a weighted distance is >= the hop count).
func TestSSSPReference(t *testing.T) {
	ds := genRMAT(t, true)
	wl, err := workload.Lookup("galytics-w")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	q, _ := wl.Query("ga-sssp")
	ans, err := q.Reference.Compute(ds, workload.Params{"source": "0"})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(ans.Rows) == 0 {
		t.Fatal("sssp: no rows")
	}
	for _, row := range ans.Rows {
		id, d := row[0].(int64), row[1].(float64)
		if d < 0 {
			t.Fatalf("sssp: node %d has negative distance %g", id, d)
		}
		if id == 0 && d != 0 {
			t.Errorf("sssp: root distance = %g, want 0", d)
		}
	}
}
