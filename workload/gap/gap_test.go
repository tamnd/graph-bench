package gap_test

import (
	"context"
	"math"
	"testing"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
	"github.com/tamnd/graph-bench/workload/galytics"
	_ "github.com/tamnd/graph-bench/workload/gap"
)

// genURand materializes a small uniform-random dataset (scale 8, always
// weighted, the GAP shape at test size) into a temp dir and opens it.
func genURand(t *testing.T) engine.Dataset {
	t.Helper()
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cfg := gen.Config{Kind: "urand", Scale: 8, EdgeFactor: 16, Seed: 7}
	if _, err := gen.Generate(context.Background(), cfg, w); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ds
}

// TestRegistered proves the workload registers with the six kernels,
// all Analytical, each naming its algorithm.
func TestRegistered(t *testing.T) {
	wl, err := workload.Lookup("gap")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !wl.Analytics {
		t.Error("Analytics = false, want true")
	}
	if wl.Dataset != "urand-14" {
		t.Errorf("Dataset = %q, want urand-14", wl.Dataset)
	}
	if wl.Fidelity != "derived" {
		t.Errorf("Fidelity = %q, want derived", wl.Fidelity)
	}
	want := map[string]string{
		"gap-bfs": "bfs", "gap-sssp": "sssp", "gap-pr": "pagerank",
		"gap-cc": "wcc", "gap-tc": "tc", "gap-bc": "bc",
	}
	if len(wl.Queries) != len(want) {
		t.Errorf("%d queries, want %d", len(wl.Queries), len(want))
	}
	for id, algo := range want {
		q, ok := wl.Query(id)
		if !ok {
			t.Errorf("query %s missing", id)
			continue
		}
		if q.Class != engine.Analytical {
			t.Errorf("%s: class = %v, want Analytical", id, q.Class)
		}
		if q.Algorithm != algo {
			t.Errorf("%s: Algorithm = %q, want %q", id, q.Algorithm, algo)
		}
		if q.Reference == nil || q.Reference.Compute == nil {
			t.Errorf("%s: nil reference Compute", id)
		}
	}
}

// TestTraversalReferences checks BFS levels and weighted SSSP distances
// from the same source: root at zero, nothing negative, and the weighted
// distance at least the hop count on every common node (weights >= 1).
func TestTraversalReferences(t *testing.T) {
	ds := genURand(t)
	wl, _ := workload.Lookup("gap")

	bfsQ, _ := wl.Query("gap-bfs")
	bfs, err := bfsQ.Reference.Compute(ds, workload.Params{"source": "0"})
	if err != nil {
		t.Fatalf("bfs Compute: %v", err)
	}
	if len(bfs.Rows) == 0 {
		t.Fatal("bfs: no rows")
	}
	levels := map[int64]int64{}
	for _, row := range bfs.Rows {
		id, level := row[0].(int64), row[1].(int64)
		if level < 0 {
			t.Fatalf("bfs: node %d has negative level %d", id, level)
		}
		levels[id] = level
	}
	if l, ok := levels[0]; !ok || l != 0 {
		t.Errorf("bfs: root level = %d (present %t), want 0", l, ok)
	}

	ssspQ, _ := wl.Query("gap-sssp")
	sssp, err := ssspQ.Reference.Compute(ds, workload.Params{"source": "0"})
	if err != nil {
		t.Fatalf("sssp Compute: %v", err)
	}
	if len(sssp.Rows) != len(bfs.Rows) {
		t.Errorf("sssp reaches %d nodes, bfs %d; same source must reach the same set", len(sssp.Rows), len(bfs.Rows))
	}
	for _, row := range sssp.Rows {
		id, d := row[0].(int64), row[1].(float64)
		if d < 0 {
			t.Fatalf("sssp: node %d has negative distance %g", id, d)
		}
		if id == 0 && d != 0 {
			t.Errorf("sssp: root distance = %g, want 0", d)
		}
		if level, ok := levels[id]; ok && d < float64(level) {
			t.Fatalf("sssp: node %d distance %g below its hop count %d with weights >= 1", id, d, level)
		}
	}
}

// TestPageRankReference checks one score per node summing to 1.
func TestPageRankReference(t *testing.T) {
	ds := genURand(t)
	g, err := workload.LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	wl, _ := workload.Lookup("gap")
	q, _ := wl.Query("gap-pr")
	ans, err := q.Reference.Compute(ds, nil)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(ans.Rows) != g.NodeCount() {
		t.Fatalf("pagerank: %d rows, want %d", len(ans.Rows), g.NodeCount())
	}
	var sum float64
	for _, row := range ans.Rows {
		sum += row[1].(float64)
	}
	if math.Abs(sum-1) > 1e-6 {
		t.Errorf("pagerank: scores sum to %g, want 1", sum)
	}
}

// TestCCReference checks the canonical labeling and its label
// invariance: at least one component, and a relabeled copy normalizes
// back to the reference through the shared normalization.
func TestCCReference(t *testing.T) {
	ds := genURand(t)
	wl, _ := workload.Lookup("gap")
	q, _ := wl.Query("gap-cc")
	ans, err := q.Reference.Compute(ds, nil)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	distinct := map[int64]bool{}
	for _, row := range ans.Rows {
		distinct[row[1].(int64)] = true
	}
	if len(distinct) < 1 {
		t.Fatal("cc: no components")
	}

	relabeled := &workload.Answer{Columns: append([]string(nil), ans.Columns...)}
	for _, row := range ans.Rows {
		relabeled.Rows = append(relabeled.Rows, []engine.Value{row[0], row[1].(int64)*5 + 11})
	}
	if err := galytics.NormalizeComponents(relabeled); err != nil {
		t.Fatalf("NormalizeComponents: %v", err)
	}
	if err := workload.Compare(relabeled, ans, q.Reference.Compare); err != nil {
		t.Fatalf("normalized relabeling does not match canonical: %v", err)
	}
}

// TestTCReference checks the single-row count is present, non-negative,
// and equal to the oracle's total.
func TestTCReference(t *testing.T) {
	ds := genURand(t)
	g, err := workload.LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	wl, _ := workload.Lookup("gap")
	q, _ := wl.Query("gap-tc")
	ans, err := q.Reference.Compute(ds, nil)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(ans.Rows) != 1 || len(ans.Rows[0]) != 1 {
		t.Fatalf("tc: answer shape %dx%d, want 1x1", len(ans.Rows), len(ans.Rows[0]))
	}
	tc := ans.Rows[0][0].(int64)
	if tc < 0 {
		t.Fatalf("tc: negative count %d", tc)
	}
	if want := g.TriangleCountTotal(); tc != want {
		t.Errorf("tc = %d, want oracle total %d", tc, want)
	}
}

// TestBCReference runs the sampled Brandes reference with a small
// source set that exists at test scale: one row per node, no negative
// score, and some positive dependency accumulated.
func TestBCReference(t *testing.T) {
	ds := genURand(t)
	g, err := workload.LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	wl, _ := workload.Lookup("gap")
	q, _ := wl.Query("gap-bc")
	if q.Reference.SampleSize != 1 {
		t.Errorf("bc SampleSize = %d, want 1", q.Reference.SampleSize)
	}
	params := workload.Params{"sources": []engine.Value{int64(0), int64(1), int64(2), int64(3)}}
	ans, err := q.Reference.Compute(ds, params)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(ans.Rows) != g.NodeCount() {
		t.Fatalf("bc: %d rows, want %d", len(ans.Rows), g.NodeCount())
	}
	var positive bool
	for _, row := range ans.Rows {
		s := row[1].(float64)
		if s < 0 {
			t.Fatalf("bc: node %v has negative centrality %g", row[0], s)
		}
		if s > 0 {
			positive = true
		}
	}
	if !positive {
		t.Error("bc: every score zero; expected some accumulated dependency on a connected graph")
	}
}
