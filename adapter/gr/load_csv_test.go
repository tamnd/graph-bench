package gr

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/target"
)

// gridDataset generates a small 4-neighbor grid into a temp directory and opens
// it as a file-backed dataset, so the gr adapter's bulk CSV load path runs
// against real canonical files. A 3x3 grid is 9 nodes and 12 edges, with every
// count known in closed form, which lets the test assert exact totals.
func gridDataset(t *testing.T) target.Dataset {
	t.Helper()
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cfg := gen.Config{Kind: "grid", Rows: 3, Cols: 3}
	if _, err := gen.Generate(context.Background(), cfg, w); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ds
}

// TestLoadCSV is the M2b milestone: a file-backed dataset loaded into gr through
// its four-pass bulk loader rather than through statements. It generates a 3x3
// grid, loads it into a disk-backed gr database, and checks the loaded counts and
// a query against the result.
func TestLoadCSV(t *testing.T) {
	ctx := context.Background()
	tg := New()
	ds := gridDataset(t)

	path := filepath.Join(t.TempDir(), "grid.gr")
	drv, err := tg.Setup(ctx, target.Config{Values: map[string]string{"path": path}})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = tg.Teardown(ctx, drv) })

	stats, err := tg.Load(ctx, drv, ds)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stats.Nodes != 9 {
		t.Errorf("loaded Nodes = %d, want 9", stats.Nodes)
	}
	if stats.Edges != 12 {
		t.Errorf("loaded Edges = %d, want 12", stats.Edges)
	}
	if stats.BytesOnDisk <= 0 {
		t.Errorf("BytesOnDisk = %d, want > 0", stats.BytesOnDisk)
	}

	// The database is reopened after the load; a count query sees the nodes the
	// loader wrote.
	q := target.Query{ID: "grid-count", Class: target.Analytical,
		Text: `MATCH (n:Node) RETURN count(n) AS c`}
	res, err := drv.Run(ctx, q, nil)
	if err != nil {
		t.Fatalf("Run count: %v", err)
	}
	rows := drain(t, res)
	if len(rows) != 1 {
		t.Fatalf("count returned %d rows, want 1", len(rows))
	}
	if c, ok := rows[0][0].(int64); !ok || c != 9 {
		t.Errorf("node count = %v (%T), want int64 9", rows[0][0], rows[0][0])
	}

	// The edges loaded into the forward CSR are traversable.
	q2 := target.Query{ID: "grid-edges", Class: target.Analytical,
		Text: `MATCH (:Node)-[r:EDGE]->(:Node) RETURN count(r) AS c`}
	res2, err := drv.Run(ctx, q2, nil)
	if err != nil {
		t.Fatalf("Run edge count: %v", err)
	}
	rows2 := drain(t, res2)
	if c, ok := rows2[0][0].(int64); !ok || c != 12 {
		t.Errorf("edge count = %v (%T), want int64 12", rows2[0][0], rows2[0][0])
	}
}

// TestLoadCSVRejectsMemory checks that a file-backed dataset cannot load into an
// in-memory gr target: the loader builds a real file, so an in-memory path is a
// clear configuration error rather than a silent no-op.
func TestLoadCSVRejectsMemory(t *testing.T) {
	ctx := context.Background()
	tg := New()
	ds := gridDataset(t)

	drv, err := tg.Setup(ctx, target.Config{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = tg.Teardown(ctx, drv) })

	if _, err := tg.Load(ctx, drv, ds); err == nil {
		t.Fatal("Load into in-memory target succeeded, want an error")
	}
}
