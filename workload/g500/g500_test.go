package g500_test

import (
	"context"
	"testing"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
	_ "github.com/tamnd/graph-bench/workload/g500"
)

// genRMATWeighted materializes a small weighted RMAT dataset (scale 8,
// the Graph500 shape at test size) into a temp dir and opens it.
func genRMATWeighted(t *testing.T) engine.Dataset {
	t.Helper()
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cfg := gen.Config{Kind: "rmat", Scale: 8, EdgeFactor: 16, Seed: 3, Weighted: true}
	if _, err := gen.Generate(context.Background(), cfg, w); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ds
}

// TestRegistered proves the workload registers both kernels, Analytical,
// sharing the eight-root pool key with deterministic draws.
func TestRegistered(t *testing.T) {
	wl, err := workload.Lookup("g500")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !wl.Analytics {
		t.Error("Analytics = false, want true")
	}
	if wl.Dataset != "rmat-14-w" {
		t.Errorf("Dataset = %q, want rmat-14-w", wl.Dataset)
	}
	if wl.Fidelity != "derived" {
		t.Errorf("Fidelity = %q, want derived", wl.Fidelity)
	}
	want := map[string]string{"g500-bfs": "bfs", "g500-sssp": "sssp"}
	if len(wl.Queries) != len(want) {
		t.Errorf("%d queries, want %d", len(wl.Queries), len(want))
	}
	var pools [][]workload.Params
	for id, algo := range want {
		q, ok := wl.Query(id)
		if !ok {
			t.Fatalf("query %s missing", id)
		}
		if q.Class != engine.Analytical {
			t.Errorf("%s: class = %v, want Analytical", id, q.Class)
		}
		if q.Algorithm != algo {
			t.Errorf("%s: Algorithm = %q, want %q", id, q.Algorithm, algo)
		}
		if q.PoolKey != "root" {
			t.Errorf("%s: PoolKey = %q, want root", id, q.PoolKey)
		}
		pool := q.Params.Pool()
		if len(pool) != 8 {
			t.Fatalf("%s: %d pooled roots, want 8", id, len(pool))
		}
		if next := q.Params.Next(); next["source"] != pool[0]["source"] {
			t.Errorf("%s: first draw %v, want pool head %v", id, next["source"], pool[0]["source"])
		}
		pools = append(pools, pool)
	}
	// Graph500 times both kernels from the same root set.
	for i := range pools[0] {
		if pools[0][i]["source"] != pools[1][i]["source"] {
			t.Errorf("root %d differs between kernels: %v vs %v", i, pools[0][i]["source"], pools[1][i]["source"])
		}
	}
}

// TestBFSKernelReference checks the K2 level array: nonempty, root at
// level zero, no negative level, and a positive traversed-edge count for
// the TEPS numerator.
func TestBFSKernelReference(t *testing.T) {
	ds := genRMATWeighted(t)
	wl, _ := workload.Lookup("g500")
	q, _ := wl.Query("g500-bfs")
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

	g, err := workload.LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	edges, ok := g.EdgesReached("0")
	if !ok || edges <= 0 {
		t.Errorf("EdgesReached(0) = %d, %t; want positive count for TEPS", edges, ok)
	}
}

// TestSSSPKernelReference checks the K3 distance array against the same
// root: root at zero, no negative distance, and the reached set equal to
// the BFS reached set (same graph, same root, weights do not change
// reachability).
func TestSSSPKernelReference(t *testing.T) {
	ds := genRMATWeighted(t)
	wl, _ := workload.Lookup("g500")

	bfsQ, _ := wl.Query("g500-bfs")
	bfs, err := bfsQ.Reference.Compute(ds, workload.Params{"source": "0"})
	if err != nil {
		t.Fatalf("bfs Compute: %v", err)
	}
	ssspQ, _ := wl.Query("g500-sssp")
	sssp, err := ssspQ.Reference.Compute(ds, workload.Params{"source": "0"})
	if err != nil {
		t.Fatalf("sssp Compute: %v", err)
	}
	if len(sssp.Rows) != len(bfs.Rows) {
		t.Errorf("sssp reaches %d nodes, bfs %d; want equal reached sets", len(sssp.Rows), len(bfs.Rows))
	}
	for _, row := range sssp.Rows {
		id, d := row[0].(int64), row[1].(float64)
		if d < 0 {
			t.Fatalf("sssp: node %d has negative distance %g", id, d)
		}
		if id == 0 && d != 0 {
			t.Errorf("sssp: root distance = %g, want 0", d)
		}
	}
}
