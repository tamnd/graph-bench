package graphalytics_test

import (
	"context"
	"testing"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
	_ "github.com/tamnd/graph-bench/workload/graphalytics"
)

// genPowerLaw materializes a small power-law dataset for the reference checks, the
// same skewed-degree shape the workload runs against.
func genPowerLaw(t *testing.T) target.Dataset {
	t.Helper()
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cfg := gen.Config{Kind: "powerlaw", N: 500, Gamma: 2.5, MinDeg: 1, MaxDeg: 100, Seed: 1}
	if _, err := gen.Generate(context.Background(), cfg, w); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ds
}

// TestGraphalyticsRegistered proves the six algorithms register, all class
// Analytical with a Cypher procedure text and a param source.
func TestGraphalyticsRegistered(t *testing.T) {
	wl, ok := workload.Lookup("graphalytics")
	if !ok {
		t.Fatal("workload graphalytics not registered")
	}
	want := []string{"ga-bfs", "ga-pagerank", "ga-wcc", "ga-cdlp", "ga-lcc", "ga-sssp"}
	if len(wl.Queries) != len(want) {
		t.Errorf("len(Queries) = %d, want %d", len(wl.Queries), len(want))
	}
	for _, id := range want {
		q, ok := wl.Query(id)
		if !ok {
			t.Errorf("query %s missing", id)
			continue
		}
		if q.Class != target.Analytical {
			t.Errorf("%s class = %v, want Analytical", id, q.Class)
		}
		if text, ok := q.Texts[workload.Cypher]; !ok || text == "" {
			t.Errorf("%s has no Cypher text", id)
		}
		if q.Params == nil {
			t.Errorf("%s has nil Params", id)
		}
		if q.Reference.Compute == nil {
			t.Errorf("%s has nil Compute; an algorithm query must be validated", id)
		}
	}
}

// TestGraphalyticsReferencesProduceRows runs every reference against a generated
// power-law dataset and checks each yields one row per node (or per reachable node
// for the traversals) with two columns, so the references are wired correctly.
func TestGraphalyticsReferencesProduceRows(t *testing.T) {
	ds := genPowerLaw(t)
	wl, _ := workload.Lookup("graphalytics")
	g, err := workload.LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	n := g.NodeCount()

	for _, q := range wl.Queries {
		ans, err := q.Reference.Compute(ds, q.Params.Next())
		if err != nil {
			t.Errorf("%s Compute: %v", q.ID, err)
			continue
		}
		if len(ans.Columns) != 2 {
			t.Errorf("%s columns = %v, want 2", q.ID, ans.Columns)
		}
		switch q.ID {
		case "ga-bfs", "ga-sssp":
			// Traversals from a source: at least the source row, at most every node.
			if len(ans.Rows) < 1 || len(ans.Rows) > n {
				t.Errorf("%s rows = %d, want between 1 and %d", q.ID, len(ans.Rows), n)
			}
		default:
			if len(ans.Rows) != n {
				t.Errorf("%s rows = %d, want %d (one per node)", q.ID, len(ans.Rows), n)
			}
		}
	}
}
