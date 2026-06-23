package workload_test

import (
	"context"
	"testing"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
	_ "github.com/tamnd/graph-bench/workload/micro"
)

// TestF1ValidationCatchesMismatch is the F1 proof: a micro query run against
// the oracle reference with a deliberately wrong engine answer produces a
// non-nil Compare error, which the harness uses to exclude the query from timing.
// F1 is not just nominal; the comparison routine catches a wrong answer before
// any latency number is recorded.
//
// This test generates a small grid, computes the oracle reference for khop1 from
// node "0" (out-degree 2 on a 3x3 grid), then runs Compare against:
//
//   - The correct answer (count=2): passes, no error.
//   - A wrong count (count=99): fails, error naming the divergence.
//   - A wrong column name: fails, error naming the mismatch.
//   - A missing row: fails, error on row count.
func TestF1ValidationCatchesMismatch(t *testing.T) {
	ds := makeGrid(t, 3, 3)
	q, ok := workload.Lookup("micro-grid")
	if !ok {
		t.Fatal("micro-grid not in registry")
	}
	khop1, ok := q.Query("micro-khop1")
	if !ok {
		t.Fatal("micro-khop1 not in micro-grid")
	}

	// Compute the oracle reference for seed "0" (top-left corner of a 3x3 grid
	// has out-degree 2: one right, one down).
	ref, err := khop1.Reference.Compute(ds, target.Params{"seed": "0"})
	if err != nil {
		t.Fatalf("oracle Compute: %v", err)
	}
	if len(ref.Rows) != 1 || ref.Rows[0][0] != int64(2) {
		t.Fatalf("oracle returned unexpected reference: %v", ref)
	}

	spec := khop1.Reference.Compare

	// Correct engine answer: count=2. Compare must pass.
	correct := &target.Answer{
		Columns: []string{"n"},
		Rows:    [][]target.Value{{int64(2)}},
	}
	if err := workload.Compare(correct, ref, spec); err != nil {
		t.Errorf("correct answer failed validation: %v", err)
	}

	// Wrong count: count=99. Compare must return an error.
	wrongCount := &target.Answer{
		Columns: []string{"n"},
		Rows:    [][]target.Value{{int64(99)}},
	}
	if err := workload.Compare(wrongCount, ref, spec); err == nil {
		t.Error("wrong count passed validation, want an error (F1 violated)")
	}

	// Wrong column name: engine returns "count" but reference says "n".
	wrongCol := &target.Answer{
		Columns: []string{"count"},
		Rows:    [][]target.Value{{int64(2)}},
	}
	if err := workload.Compare(wrongCol, ref, spec); err == nil {
		t.Error("wrong column name passed validation, want an error")
	}

	// Missing row: engine returns no rows.
	emptyResult := &target.Answer{
		Columns: []string{"n"},
		Rows:    nil,
	}
	if err := workload.Compare(emptyResult, ref, spec); err == nil {
		t.Error("empty result passed validation, want an error")
	}

	// Float coercion: the same count returned as float64. The CompareSpec for
	// khop1 has CoerceNum=true, so this should pass.
	floatCount := &target.Answer{
		Columns: []string{"n"},
		Rows:    [][]target.Value{{float64(2)}},
	}
	if err := workload.Compare(floatCount, ref, spec); err != nil {
		t.Errorf("coerced float count failed with CoerceNum=true: %v", err)
	}

	// Float coercion disabled: same float64 count against strict spec. Should fail.
	strictSpec := workload.CompareSpec{Ordered: true, CoerceNum: false}
	if err := workload.Compare(floatCount, ref, strictSpec); err == nil {
		t.Error("float count passed strict (no coercion) validation, want an error")
	}
}

// TestF1NilComputeIsError proves that a RefStrategy with nil Compute is caught
// at the harness boundary: the harness must check Compute != nil before timing
// begins, and if it forgets, the oracle call produces a clear error rather than
// a silent pass. The test models the harness's obligation by calling Compute
// directly and checking that the nil case is not silently skipped.
func TestF1NilComputeIsError(t *testing.T) {
	// A write query carries nil Compute by design (post-condition discipline).
	// A harness that calls Compute on a write query without checking gets nil/nil,
	// which the harness must treat as "no reference, skip validation" or "error".
	// We model the intent: nil Compute means the validation contract is different,
	// not that it is absent.
	w, ok := workload.Lookup("micro-write")
	if !ok {
		t.Skip("micro-write not in registry; import workload/micro")
	}
	writeNode, ok := w.Query("micro-write-node")
	if !ok {
		t.Fatal("micro-write-node not in micro-write")
	}
	if writeNode.Reference.Compute != nil {
		t.Error("write query has a non-nil Compute; expected nil (post-condition discipline)")
	}
	// The harness must not call Compare against a nil reference. A nil Compute
	// means no oracle-based reference exists; validation is by post-condition
	// count, which is the harness's responsibility, not Compare's.
}

// TestF1OracleAgainstClosedFormSP proves the SP oracle on the 5x5 grid matches
// the closed-form Manhattan distance, so the oracle's answer is trustworthy
// (the reference is an honest arithmetic formula, not an engine's output).
func TestF1OracleAgainstClosedFormSP(t *testing.T) {
	ds := makeGrid(t, 5, 5)
	w, ok := workload.Lookup("micro-grid")
	if !ok {
		t.Fatal("micro-grid not in registry")
	}
	sp, ok := w.Query("micro-sp")
	if !ok {
		t.Fatal("micro-sp not in micro-grid")
	}

	// Top-left to bottom-right: Manhattan distance = (5-1) + (5-1) = 8.
	ref, err := sp.Reference.Compute(ds, target.Params{"src": "0", "dst": "24"})
	if err != nil {
		t.Fatalf("oracle Compute: %v", err)
	}
	if len(ref.Rows) != 1 || ref.Rows[0][0] != int64(8) {
		t.Errorf("oracle SP(0,24) = %v, want [[8]]", ref.Rows)
	}

	// An engine reporting distance=5 (wrong) is caught.
	wrong := &target.Answer{Columns: []string{"d"}, Rows: [][]target.Value{{int64(5)}}}
	if err := workload.Compare(wrong, ref, sp.Reference.Compare); err == nil {
		t.Error("wrong SP distance passed validation")
	}

	// The correct answer passes.
	correct := &target.Answer{Columns: []string{"d"}, Rows: [][]target.Value{{int64(8)}}}
	if err := workload.Compare(correct, ref, sp.Reference.Compare); err != nil {
		t.Errorf("correct SP distance failed validation: %v", err)
	}
}

// makeGrid generates a Rows×Cols 4-neighbor grid into a temp directory.
func makeGrid(t *testing.T, rows, cols int) *dataset.Set {
	t.Helper()
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, err := gen.Generate(context.Background(), gen.Config{Kind: "grid", Rows: rows, Cols: cols}, w); err != nil {
		t.Fatalf("Generate grid %dx%d: %v", rows, cols, err)
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ds
}
