package measure

import (
	"testing"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// mixedPools builds a perQuery map with n distinct ready-made ops per id, so
// tests can check both interleaving and op cycling.
func mixedPools(ids []string, n int) map[string][]engine.Op {
	pools := make(map[string][]engine.Op, len(ids))
	for _, id := range ids {
		ops := make([]engine.Op, n)
		for i := range ops {
			ops[i] = engine.Op{QueryID: id, Class: engine.PointRead, Dialect: engine.Cypher, Text: "RETURN 1"}
		}
		pools[id] = ops
	}
	return pools
}

// TestBuildMixedScheduleDeterministic proves two builds with the same inputs
// and seed produce identical schedules — the seed is a stamp field (MixSeed),
// so the schedule must be a pure function of it (spec 08 §7).
func TestBuildMixedScheduleDeterministic(t *testing.T) {
	ids := []string{"q1", "q2", "q3"}
	weights := map[string]float64{"q1": 2.0, "q2": 1.0, "q3": 0.5}
	pools := mixedPools(ids, 4)

	a := BuildMixedSchedule(pools, weights, 42, 200, 100, 0)
	b := BuildMixedSchedule(pools, weights, 42, 200, 100, 0)

	if len(a) != len(b) {
		t.Fatalf("lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Op.QueryID != b[i].Op.QueryID || a[i].Offset != b[i].Offset {
			t.Fatalf("schedules diverge at %d: %q@%v vs %q@%v",
				i, a[i].Op.QueryID, a[i].Offset, b[i].Op.QueryID, b[i].Offset)
		}
	}

	// A different seed should produce a different interleave (overwhelmingly
	// likely over 200 draws from 3 queries).
	c := BuildMixedSchedule(pools, weights, 43, 200, 100, 0)
	same := true
	for i := range a {
		if a[i].Op.QueryID != c[i].Op.QueryID {
			same = false
			break
		}
	}
	if same {
		t.Error("seed 42 and seed 43 produced identical interleaves")
	}
}

// TestBuildMixedScheduleCount proves the schedule contains exactly totalCount ops.
func TestBuildMixedScheduleCount(t *testing.T) {
	ids := []string{"q1", "q2", "q3"}
	weights := map[string]float64{"q1": 2.0, "q2": 1.0, "q3": 1.0}
	ops := BuildMixedSchedule(mixedPools(ids, 2), weights, 1, 100, 10, 0)
	if len(ops) != 100 {
		t.Errorf("op count=%d, want 100", len(ops))
	}
}

// TestBuildMixedScheduleQueryIDs proves every op carries its source query ID.
func TestBuildMixedScheduleQueryIDs(t *testing.T) {
	ids := []string{"qa", "qb"}
	weights := map[string]float64{"qa": 1.0, "qb": 1.0}
	ops := BuildMixedSchedule(mixedPools(ids, 3), weights, 7, 20, 5, 0)
	for _, op := range ops {
		if op.Op.QueryID != "qa" && op.Op.QueryID != "qb" {
			t.Errorf("op.Op.QueryID=%q not in {qa, qb}", op.Op.QueryID)
		}
	}
}

// TestBuildMixedScheduleWeightedRatio proves that with weights 3:1 the higher
// weighted query appears roughly 3x more often. The schedule is deterministic
// for a fixed seed, so the tolerance only absorbs sampling noise at n=400.
func TestBuildMixedScheduleWeightedRatio(t *testing.T) {
	ids := []string{"heavy", "light"}
	weights := map[string]float64{"heavy": 3.0, "light": 1.0}
	ops := BuildMixedSchedule(mixedPools(ids, 5), weights, 42, 400, 50, 0)
	var heavyCount, lightCount int
	for _, op := range ops {
		switch op.Op.QueryID {
		case "heavy":
			heavyCount++
		case "light":
			lightCount++
		}
	}
	ratio := float64(heavyCount) / float64(lightCount)
	if ratio < 2.4 || ratio > 3.7 {
		t.Errorf("heavy:light ratio=%.2f (%d:%d), want ~3.0", ratio, heavyCount, lightCount)
	}
}

// TestBuildMixedScheduleOffsets proves Offset values are set and monotonically
// non-decreasing (the schedule is time-ordered).
func TestBuildMixedScheduleOffsets(t *testing.T) {
	ids := []string{"q1", "q2"}
	weights := map[string]float64{"q1": 1.0, "q2": 1.0}
	ops := BuildMixedSchedule(mixedPools(ids, 2), weights, 3, 20, 10, 0)
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

// TestBuildMixedScheduleEmptyWeights proves empty weights or ids with no ops
// return nil rather than panicking.
func TestBuildMixedScheduleEmptyWeights(t *testing.T) {
	if ops := BuildMixedSchedule(nil, nil, 1, 100, 10, 0); ops != nil {
		t.Errorf("expected nil for empty weights, got %d ops", len(ops))
	}
	// Weights present but no ops behind them.
	weights := map[string]float64{"ghost": 1.0}
	if ops := BuildMixedSchedule(map[string][]engine.Op{}, weights, 1, 100, 10, 0); ops != nil {
		t.Errorf("expected nil for weights without ops, got %d ops", len(ops))
	}
}

// TestBuildMixedScheduleCyclesOps proves that when the draw count exceeds the
// ops provided for a query, its ops are reused in order (cycling), so a small
// parameter pool still fills a long schedule.
func TestBuildMixedScheduleCyclesOps(t *testing.T) {
	pools := map[string][]engine.Op{
		"only": {
			{QueryID: "only", Text: "A"},
			{QueryID: "only", Text: "B"},
		},
	}
	weights := map[string]float64{"only": 1.0}
	ops := BuildMixedSchedule(pools, weights, 5, 6, 0, 0)
	if len(ops) != 6 {
		t.Fatalf("len=%d, want 6", len(ops))
	}
	want := []string{"A", "B", "A", "B", "A", "B"}
	for i, op := range ops {
		if op.Op.Text != want[i] {
			t.Errorf("ops[%d].Text=%q, want %q (in-order cycling)", i, op.Op.Text, want[i])
		}
	}
}

// TestMixedResultInterference proves Interference() returns a ratio based on
// the isolated vs mixed p99.
func TestMixedResultInterference(t *testing.T) {
	isoResult := Result{
		ByQuery: map[string]Stat{
			"q1": {Class: engine.PointRead, Count: 10, P99: 2 * time.Millisecond},
		},
	}
	mixResult := MixedResult{
		Result: Result{
			ByQuery: map[string]Stat{
				"q1": {Class: engine.PointRead, Count: 10, P99: 6 * time.Millisecond},
			},
		},
		IsolatedByQuery: map[string]Result{
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
	mr := MixedResult{
		Result: Result{
			ByQuery: map[string]Stat{
				"q1": {P99: 5 * time.Millisecond},
			},
		},
	}
	if f := mr.Interference("q1"); f != 0 {
		t.Errorf("Interference=%f, want 0 (no isolated result)", f)
	}
}
