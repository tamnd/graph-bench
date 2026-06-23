package measure

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tamnd/graph-bench/target"
)

// TestColdRunRecordsColdMap checks that ColdRun populates Result.Cold and
// leaves Result.Stats nil.
func TestColdRunRecordsColdMap(t *testing.T) {
	d := &fakeDriver{}
	ops := []Op{
		{Class: target.PointRead, Query: target.Query{ID: "pr1"}},
		{Class: target.Traversal, Query: target.Query{ID: "tr1"}},
	}

	res := ColdRun(context.Background(), d, ops, 0)

	if res.Stats != nil {
		t.Error("ColdRun should leave Stats nil; caller merges")
	}
	if res.Cold == nil {
		t.Fatal("ColdRun did not populate Cold map")
	}
	if _, ok := res.Cold[target.PointRead]; !ok {
		t.Error("Cold missing PointRead class")
	}
	if _, ok := res.Cold[target.Traversal]; !ok {
		t.Error("Cold missing Traversal class")
	}
}

// TestColdRunCountsOpsPerClass proves Count increments for every op in the class,
// not just distinct query IDs.
func TestColdRunCountsOpsPerClass(t *testing.T) {
	d := &fakeDriver{}
	ops := []Op{
		{Class: target.Traversal},
		{Class: target.Traversal},
		{Class: target.Traversal},
	}

	res := ColdRun(context.Background(), d, ops, 0)

	stat := res.Cold[target.Traversal]
	if stat.Count != 3 {
		t.Errorf("Cold[Traversal].Count = %d, want 3", stat.Count)
	}
	if d.calls.Load() != 3 {
		t.Errorf("driver called %d times, want 3", d.calls.Load())
	}
}

// TestColdRunErrorCounting proves errors are counted in the Cold stat and the
// P99/Max are not set from failed ops.
func TestColdRunErrorCounting(t *testing.T) {
	d := &fakeDriver{err: errors.New("cold fail")}
	ops := []Op{
		{Class: target.PointRead},
		{Class: target.PointRead},
	}

	res := ColdRun(context.Background(), d, ops, 0)

	stat := res.Cold[target.PointRead]
	if stat.Count != 2 {
		t.Errorf("Count = %d, want 2", stat.Count)
	}
	if stat.Errors != 2 {
		t.Errorf("Errors = %d, want 2", stat.Errors)
	}
	if stat.P99 != 0 || stat.Max != 0 {
		t.Errorf("P99/Max non-zero on all-error cold run: P99=%v Max=%v", stat.P99, stat.Max)
	}
}

// TestColdRunSequentialOrder proves ColdRun executes ops sequentially (not in
// parallel), which is required to avoid warming the engine for later ops.
// We verify this by checking total elapsed time: if 3 ops each take 5ms
// sequentially the total must be at least 15ms; in parallel it would be ~5ms.
func TestColdRunSequentialOrder(t *testing.T) {
	d := &fakeDriver{latency: 5 * time.Millisecond}
	ops := []Op{
		{Class: target.Traversal},
		{Class: target.Traversal},
		{Class: target.Traversal},
	}

	start := time.Now()
	ColdRun(context.Background(), d, ops, 0)
	elapsed := time.Since(start)

	// Sequential: at least 3 * 5ms = 15ms.
	if elapsed < 15*time.Millisecond {
		t.Errorf("elapsed %v looks parallel (want >= 15ms for 3 x 5ms serial ops)", elapsed)
	}
}

// TestColdRunContextCancel proves that cancelling the context stops ColdRun
// early (no hang).
func TestColdRunContextCancel(t *testing.T) {
	d := &fakeDriver{latency: 10 * time.Millisecond}
	ops := makeOps(20, target.Traversal)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	res := ColdRun(ctx, d, ops, 0)

	// At most ~2-3 ops should have run before the timeout.
	total := res.Cold[target.Traversal].Count
	if total >= 20 {
		t.Errorf("ColdRun ran %d ops despite context cancel, expected early stop", total)
	}
}

// TestMergeCold proves MergeCold sets the warm Result.Cold from the cold Result.
func TestMergeCold(t *testing.T) {
	warm := Result{
		Stats: map[target.Class]Stat{
			target.Traversal: {Count: 100},
		},
	}
	coldRes := Result{
		Cold: map[target.Class]Stat{
			target.Traversal: {Count: 1, P99: 200 * time.Millisecond},
		},
	}

	merged := MergeCold(warm, coldRes)

	if merged.Stats[target.Traversal].Count != 100 {
		t.Error("MergeCold lost the warm Stats")
	}
	if merged.Cold == nil {
		t.Fatal("MergeCold did not attach Cold map")
	}
	if merged.Cold[target.Traversal].P99 != 200*time.Millisecond {
		t.Errorf("Cold P99 = %v, want 200ms", merged.Cold[target.Traversal].P99)
	}
}
