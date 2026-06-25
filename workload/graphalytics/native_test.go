package graphalytics_test

import (
	"context"
	"testing"

	gradapter "github.com/tamnd/graph-bench/adapter/gr"
	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
)

// runGrNative loads the dataset into a fresh gr database through the bulk loader
// and runs one query, returning the rows as an Answer for comparison against the
// oracle. The bulk loader needs a disk path, so the database lives in the test's
// temp dir.
func runGrNative(t *testing.T, ds target.Dataset, cols []string, text string, params target.Params) *target.Answer {
	t.Helper()
	tgt := gradapter.New()
	ctx := context.Background()
	drv, err := tgt.Setup(ctx, target.Config{Values: map[string]string{"path": t.TempDir() + "/native.gr"}})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer func() { _ = tgt.Teardown(ctx, drv) }()
	if _, err := tgt.Load(ctx, drv, ds); err != nil {
		t.Fatalf("Load: %v", err)
	}
	res, err := drv.Run(ctx, target.Query{Text: text}, params)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var rows [][]target.Value
	for res.Next() {
		src := res.Row()
		row := make([]target.Value, len(src))
		copy(row, src)
		rows = append(rows, row)
	}
	if err := res.Err(); err != nil {
		t.Fatalf("iter: %v", err)
	}
	_ = res.Close()
	return &target.Answer{Columns: cols, Rows: rows}
}

// TestGrNativeAlgorithms runs the registered GrCypher text of every Graphalytics
// query through gr's native algo_* functions and validates the result against the
// engine-independent oracle. This is the end-to-end proof that gr fills every
// analytical cell with a correct number rather than deferring the algorithm.
func TestGrNativeAlgorithms(t *testing.T) {
	ds := genPowerLaw(t)
	wl, ok := workload.Lookup("graphalytics")
	if !ok {
		t.Fatal("workload graphalytics not registered")
	}

	for _, q := range wl.Queries {
		q := q
		t.Run(q.ID, func(t *testing.T) {
			text, ok := q.Texts[workload.GrCypher]
			if !ok || text == "" {
				t.Fatalf("%s has no GrCypher text; gr would defer this algorithm", q.ID)
			}
			params := q.Params.Next()
			want, err := q.Reference.Compute(ds, params)
			if err != nil {
				t.Fatalf("%s oracle: %v", q.ID, err)
			}
			got := runGrNative(t, ds, want.Columns, text, params)
			if err := workload.Compare(got, want, q.Reference.Compare); err != nil {
				t.Errorf("%s native result does not match the oracle: %v", q.ID, err)
			}
		})
	}
}
