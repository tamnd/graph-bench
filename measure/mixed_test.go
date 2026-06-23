package measure_test

import (
	"context"
	"testing"
	"time"

	"github.com/tamnd/graph-bench/measure"
	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
)

// makeTestWorkload builds a minimal workload with a Mix for testing.
func makeTestWorkload(ids []string, weights map[string]float64) *workload.Workload {
	queries := make([]*workload.WorkloadQuery, len(ids))
	for i, id := range ids {
		queries[i] = &workload.WorkloadQuery{
			ID:    id,
			Class: target.PointRead,
			Texts: map[workload.Dialect]string{
				workload.Cypher: "RETURN 1",
			},
			Params: workload.NewFixed(nil),
			Reference: workload.RefStrategy{
				Compare: workload.CompareSpec{},
			},
		}
	}
	return &workload.Workload{
		Name:    "test",
		Queries: queries,
		Mix:     weights,
	}
}

// TestBuildMixedScheduleCount proves the schedule contains approximately
// totalCount ops, interleaved from queries in proportion to their Mix weights.
func TestBuildMixedScheduleCount(t *testing.T) {
	ids := []string{"q1", "q2", "q3"}
	weights := map[string]float64{"q1": 2.0, "q2": 1.0, "q3": 1.0}
	wl := makeTestWorkload(ids, weights)
	totalCount := 100
	ops := measure.BuildMixedSchedule(wl, workload.Cypher, totalCount, 10, 0)
	// Rounding may push total off by a few; allow ±len(queries).
	if diff := len(ops) - totalCount; diff < -3 || diff > 3 {
		t.Errorf("op count=%d, want ~%d (delta=%d)", len(ops), totalCount, diff)
	}
}

// TestBuildMixedScheduleQueryIDs proves every op carries its source query ID.
func TestBuildMixedScheduleQueryIDs(t *testing.T) {
	ids := []string{"qa", "qb"}
	weights := map[string]float64{"qa": 1.0, "qb": 1.0}
	wl := makeTestWorkload(ids, weights)
	ops := measure.BuildMixedSchedule(wl, workload.Cypher, 20, 5, 0)
	for _, op := range ops {
		if op.QueryID != "qa" && op.QueryID != "qb" {
			t.Errorf("op.QueryID=%q not in {qa, qb}", op.QueryID)
		}
	}
}

// TestBuildMixedScheduleWeightedRatio proves that with weights 3:1 the higher
// weighted query appears roughly 3x more often.
func TestBuildMixedScheduleWeightedRatio(t *testing.T) {
	ids := []string{"heavy", "light"}
	weights := map[string]float64{"heavy": 3.0, "light": 1.0}
	wl := makeTestWorkload(ids, weights)
	ops := measure.BuildMixedSchedule(wl, workload.Cypher, 400, 50, 0)
	var heavyCount, lightCount int
	for _, op := range ops {
		switch op.QueryID {
		case "heavy":
			heavyCount++
		case "light":
			lightCount++
		}
	}
	// Allow 10% tolerance on the 3:1 ratio.
	ratio := float64(heavyCount) / float64(lightCount)
	if ratio < 2.7 || ratio > 3.3 {
		t.Errorf("heavy:light ratio=%.2f, want ~3.0 (±10%%)", ratio)
	}
}

// TestBuildMixedScheduleOffsets proves Offset values are set and strictly
// monotonically non-decreasing (the schedule is time-ordered).
func TestBuildMixedScheduleOffsets(t *testing.T) {
	ids := []string{"q1", "q2"}
	weights := map[string]float64{"q1": 1.0, "q2": 1.0}
	wl := makeTestWorkload(ids, weights)
	ops := measure.BuildMixedSchedule(wl, workload.Cypher, 20, 10, 0)
	if len(ops) == 0 {
		t.Fatal("no ops returned")
	}
	for i := 1; i < len(ops); i++ {
		if ops[i].Offset < ops[i-1].Offset {
			t.Errorf("offset[%d]=%v < offset[%d]=%v (not monotone)", i, ops[i].Offset, i-1, ops[i-1].Offset)
		}
	}
	if ops[0].Offset < 0 {
		t.Error("first op has negative Offset")
	}
}

// TestBuildMixedScheduleEmptyMix proves an empty Mix returns nil.
func TestBuildMixedScheduleEmptyMix(t *testing.T) {
	wl := &workload.Workload{Name: "empty", Mix: nil}
	ops := measure.BuildMixedSchedule(wl, workload.Cypher, 100, 10, 0)
	if ops != nil {
		t.Errorf("expected nil for empty Mix, got %d ops", len(ops))
	}
}

// TestBuildIsolatedOpsCount proves the op count equals the requested count.
func TestBuildIsolatedOpsCount(t *testing.T) {
	q := &workload.WorkloadQuery{
		ID:     "iso-q",
		Class:  target.Traversal,
		Texts:  map[workload.Dialect]string{workload.Cypher: "RETURN 2"},
		Params: workload.NewFixed(nil),
	}
	ops := measure.BuildIsolatedOps(q, workload.Cypher, 50)
	if len(ops) != 50 {
		t.Errorf("len(ops)=%d, want 50", len(ops))
	}
}

// TestBuildIsolatedOpsQueryID proves every op carries the query's ID.
func TestBuildIsolatedOpsQueryID(t *testing.T) {
	q := &workload.WorkloadQuery{
		ID:     "iso-test",
		Class:  target.Traversal,
		Texts:  map[workload.Dialect]string{workload.Cypher: "RETURN 1"},
		Params: workload.NewFixed(nil),
	}
	ops := measure.BuildIsolatedOps(q, workload.Cypher, 10)
	for _, op := range ops {
		if op.QueryID != "iso-test" {
			t.Errorf("op.QueryID=%q, want iso-test", op.QueryID)
		}
		if op.Class != target.Traversal {
			t.Errorf("op.Class=%v, want Traversal", op.Class)
		}
	}
}

// TestByQueryPopulated proves that running ops with QueryID set populates
// Result.ByQuery.
func TestByQueryPopulated(t *testing.T) {
	d := &echoDriver{}
	ops := []measure.Op{
		{Class: target.PointRead, QueryID: "q-alpha", Query: target.Query{Text: "RETURN 1"}, Params: nil},
		{Class: target.PointRead, QueryID: "q-beta", Query: target.Query{Text: "RETURN 2"}, Params: nil},
		{Class: target.PointRead, QueryID: "q-alpha", Query: target.Query{Text: "RETURN 1"}, Params: nil},
	}
	ops = measure.BuildSchedule(ops, 10, 0)
	res := measure.Run(t.Context(), d, ops, measure.Options{Concurrency: 1, Count: len(ops)})
	if res.ByQuery == nil {
		t.Fatal("ByQuery is nil; QueryID was set on ops")
	}
	if _, ok := res.ByQuery["q-alpha"]; !ok {
		t.Error("ByQuery missing q-alpha")
	}
	if _, ok := res.ByQuery["q-beta"]; !ok {
		t.Error("ByQuery missing q-beta")
	}
	if res.ByQuery["q-alpha"].Count != 2 {
		t.Errorf("q-alpha Count=%d, want 2", res.ByQuery["q-alpha"].Count)
	}
}

// TestMixedResultInterference proves Interference() returns a ratio based on
// the isolated vs mixed p99.
func TestMixedResultInterference(t *testing.T) {
	isoResult := measure.Result{
		ByQuery: map[string]measure.Stat{
			"q1": {Class: target.PointRead, Count: 10, P99: 2 * time.Millisecond},
		},
	}
	mixResult := measure.MixedResult{
		Result: measure.Result{
			ByQuery: map[string]measure.Stat{
				"q1": {Class: target.PointRead, Count: 10, P99: 6 * time.Millisecond},
			},
		},
		IsolatedByQuery: map[string]measure.Result{
			"q1": isoResult,
		},
	}
	factor := mixResult.Interference("q1")
	// 6ms / 2ms = 3.0
	if factor < 2.9 || factor > 3.1 {
		t.Errorf("Interference=%f, want ~3.0", factor)
	}
}

// TestMixedResultInterferenceMissing proves Interference() returns 0 when the
// isolated result is not set.
func TestMixedResultInterferenceMissing(t *testing.T) {
	mr := measure.MixedResult{
		Result: measure.Result{
			ByQuery: map[string]measure.Stat{
				"q1": {P99: 5 * time.Millisecond},
			},
		},
	}
	if f := mr.Interference("q1"); f != 0 {
		t.Errorf("Interference=%f, want 0 (no isolated result)", f)
	}
}

// echoDriver is a trivial driver that immediately returns an empty result.
type echoDriver struct{}

func (d *echoDriver) Run(_ context.Context, _ target.Query, _ target.Params) (target.Result, error) {
	return &emptyResult{}, nil
}
func (d *echoDriver) Begin(_ context.Context, _ target.AccessMode) (target.Tx, error) {
	return nil, nil
}
func (d *echoDriver) Close(_ context.Context) error { return nil }

type emptyResult struct{ done bool }

func (r *emptyResult) Columns() []string   { return nil }
func (r *emptyResult) Next() bool          { return false }
func (r *emptyResult) Row() []target.Value { return nil }
func (r *emptyResult) Err() error          { return nil }
func (r *emptyResult) Close() error        { return nil }
