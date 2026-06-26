package graph500_test

import (
	"context"
	"testing"
	"time"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/measure"
	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
	"github.com/tamnd/graph-bench/workload/graph500"
	_ "github.com/tamnd/graph-bench/workload/graph500"
)

// genRMAT materializes a small RMAT dataset, the Graph500 input shape.
func genRMAT(t *testing.T) target.Dataset {
	t.Helper()
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cfg := gen.Config{Kind: "rmat", Scale: 12, EdgeFactor: 16, Seed: 1}
	if _, err := gen.Generate(context.Background(), cfg, w); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ds
}

// TestGraph500Registered proves the kernel registers as one Traversal query with a
// Cypher text, a Kuzu variant, a source parameter, and a validated reference.
func TestGraph500Registered(t *testing.T) {
	wl, ok := workload.Lookup("graph500")
	if !ok {
		t.Fatal("workload graph500 not registered")
	}
	if len(wl.Queries) != 1 {
		t.Fatalf("len(Queries) = %d, want 1", len(wl.Queries))
	}
	q := wl.Queries[0]
	if q.ID != "g500-bfs" {
		t.Errorf("id = %q, want g500-bfs", q.ID)
	}
	if q.Class != target.Traversal {
		t.Errorf("class = %v, want Traversal", q.Class)
	}
	if q.Texts[workload.Cypher] == "" {
		t.Error("missing Cypher text")
	}
	if q.Texts[workload.KuzuCypher] == "" {
		t.Error("missing Kuzu variant")
	}
	if q.Reference.Compute == nil {
		t.Error("nil Compute; the kernel must be validated")
	}
}

// TestGraph500ReferenceAndTEPS runs the BFS reference and the TEPS numerator on an
// RMAT dataset, checking the levels are all at least one (the root row is dropped)
// and that a measured duration yields a positive rate.
func TestGraph500ReferenceAndTEPS(t *testing.T) {
	ds := genRMAT(t)
	wl, _ := workload.Lookup("graph500")
	q := wl.Queries[0]

	ans, err := q.Reference.Compute(ds, q.Params.Next())
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(ans.Rows) == 0 {
		t.Fatal("BFS reached no node; RMAT root 0 should reach some")
	}
	for _, row := range ans.Rows {
		if lvl, ok := row[1].(int64); !ok || lvl < 1 {
			t.Errorf("level %v should be an int64 >= 1 (root row dropped)", row[1])
		}
	}

	edges, err := graph500.ExpectedEdges(ds)
	if err != nil {
		t.Fatalf("ExpectedEdges: %v", err)
	}
	if edges <= 0 {
		t.Fatalf("ExpectedEdges = %d, want > 0", edges)
	}
	rate := measure.TEPS(edges, 10*time.Millisecond)
	if rate <= 0 {
		t.Errorf("TEPS = %g, want > 0", rate)
	}
}
