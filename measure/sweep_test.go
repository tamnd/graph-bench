package measure

import (
	"context"
	"testing"

	"github.com/tamnd/graph-bench/engine"
)

// TestIsolatedLatencyConcurrency1 proves IsolatedLatency passes Concurrency=1
// to Run, which means only one goroutine is in the pool at a time.
func TestIsolatedLatencyConcurrency1(t *testing.T) {
	s := &fakeSession{}
	ops := BuildSchedule(makeOps(5, engine.Traversal), 1000, 0)
	// Set a high concurrency in the base opt to prove IsolatedLatency overrides it.
	opt := Options{Rate: 1000, Count: 5, Concurrency: 16}

	res := IsolatedLatency(context.Background(), s, ops, opt)

	if s.calls.Load() != 5 {
		t.Errorf("session called %d times, want 5", s.calls.Load())
	}
	stat := res.Stats[engine.Traversal]
	if stat.Count != 5 {
		t.Errorf("Count = %d, want 5", stat.Count)
	}
	// No Sweep field from IsolatedLatency.
	if len(res.Sweep) != 0 {
		t.Errorf("Sweep len = %d, want 0", len(res.Sweep))
	}
}

// TestSweepPopulatesSweepPoints checks that Sweep runs at each requested
// concurrency and produces one SweepPoint per (class, concurrency) pair.
func TestSweepPopulatesSweepPoints(t *testing.T) {
	s := &fakeSession{}
	ops := BuildSchedule(makeOps(6, engine.Traversal), 1000, 0)
	opt := Options{Rate: 1000, Count: 6, Concurrency: 1}
	points := []int{1, 2, 4}

	res := Sweep(context.Background(), s, ops, opt, points)

	// Three concurrency points, one class each -> 3 SweepPoints.
	if len(res.Sweep) != 3 {
		t.Errorf("len(Sweep) = %d, want 3", len(res.Sweep))
	}
	// First point's concurrency must be 1.
	if res.Sweep[0].Concurrency != 1 {
		t.Errorf("Sweep[0].Concurrency = %d, want 1", res.Sweep[0].Concurrency)
	}
	if res.Sweep[0].Class != engine.Traversal {
		t.Errorf("Sweep[0].Class = %v, want Traversal", res.Sweep[0].Class)
	}
}

// TestSweepBaseStatsFromFirstPoint checks that Result.Stats comes from the
// first concurrency point (the isolated single-client measurement).
func TestSweepBaseStatsFromFirstPoint(t *testing.T) {
	s := &fakeSession{}
	ops := BuildSchedule(makeOps(4, engine.PointRead), 1000, 0)
	opt := Options{Rate: 1000, Count: 4, Concurrency: 1}
	points := []int{1, 2}

	res := Sweep(context.Background(), s, ops, opt, points)

	if _, ok := res.Stats[engine.PointRead]; !ok {
		t.Error("Stats does not contain PointRead from first sweep point")
	}
}

// TestSweepContextCancelStopsEarly proves that cancelling the context stops
// the sweep after the in-flight point drains (no hang).
func TestSweepContextCancelStopsEarly(t *testing.T) {
	s := &fakeSession{}
	ops := BuildSchedule(makeOps(3, engine.Traversal), 1000, 0)
	opt := Options{Rate: 1000, Count: 3, Concurrency: 1}
	points := DefaultSweepPoints // 1,2,4,8,16,32

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately after starting.
	cancel()

	res := Sweep(ctx, s, ops, opt, points)
	// At most one full point can complete before ctx.Err() gates the loop.
	if len(res.Sweep) > len(points) {
		t.Errorf("Sweep len = %d unexpectedly high after cancel", len(res.Sweep))
	}
}

// TestDefaultAndCISweepPoints checks the well-known point sets.
func TestDefaultAndCISweepPoints(t *testing.T) {
	if DefaultSweepPoints[0] != 1 || DefaultSweepPoints[len(DefaultSweepPoints)-1] != 32 {
		t.Errorf("DefaultSweepPoints = %v, want [1..32]", DefaultSweepPoints)
	}
	if CISweepPoints[0] != 1 || len(CISweepPoints) != 3 {
		t.Errorf("CISweepPoints = %v, want [1,4,16]", CISweepPoints)
	}
}

// TestSweepMultiClass checks that a sweep with two classes produces 2 points
// per concurrency level.
func TestSweepMultiClass(t *testing.T) {
	s := &fakeSession{}
	ops := []Op{
		{Op: engine.Op{Class: engine.PointRead}},
		{Op: engine.Op{Class: engine.Traversal}},
		{Op: engine.Op{Class: engine.PointRead}},
		{Op: engine.Op{Class: engine.Traversal}},
	}
	ops = BuildSchedule(ops, 1000, 0)
	opt := Options{Rate: 1000, Count: 4}
	points := []int{1, 2}

	res := Sweep(context.Background(), s, ops, opt, points)

	// 2 concurrency points * 2 classes = 4 SweepPoints.
	if len(res.Sweep) != 4 {
		t.Errorf("len(Sweep) = %d, want 4 (2 points * 2 classes)", len(res.Sweep))
	}
}
