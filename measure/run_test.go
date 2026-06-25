package measure

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tamnd/graph-bench/target"
)

// fakeResult is a target.Result that returns a fixed number of empty rows.
type fakeResult struct {
	n   int
	cur int
}

func (r *fakeResult) Columns() []string   { return []string{"x"} }
func (r *fakeResult) Next() bool          { r.cur++; return r.cur <= r.n }
func (r *fakeResult) Row() []target.Value { return []target.Value{nil} }
func (r *fakeResult) Err() error          { return nil }
func (r *fakeResult) Close() error        { return nil }

// fakeDriver counts calls and optionally adds latency or returns errors.
type fakeDriver struct {
	calls   atomic.Int64
	latency time.Duration // artificial sleep per query
	err     error         // if non-nil, returned on every Run
}

func (d *fakeDriver) Run(_ context.Context, _ target.Query, _ target.Params) (target.Result, error) {
	d.calls.Add(1)
	if d.latency > 0 {
		time.Sleep(d.latency)
	}
	if d.err != nil {
		return nil, d.err
	}
	return &fakeResult{n: 3}, nil
}

func (d *fakeDriver) Begin(_ context.Context, _ target.AccessMode) (target.Tx, error) {
	return nil, errors.New("not implemented")
}
func (d *fakeDriver) Close(_ context.Context) error { return nil }

// makeOps builds n ops for the given class with no params and an empty Query.
func makeOps(n int, class target.Class) []Op {
	ops := make([]Op, n)
	for i := range ops {
		ops[i] = Op{Class: class}
	}
	return ops
}

// TestRunAllSteady checks that Run with no warmup records every op as a steady
// sample and produces a Stat with the right Count.
func TestRunAllSteady(t *testing.T) {
	d := &fakeDriver{}
	ops := BuildSchedule(makeOps(5, target.Traversal), 1000, 0)
	opt := Options{Rate: 1000, Count: 5, Concurrency: 1}

	res := Run(context.Background(), d, ops, opt)

	if d.calls.Load() != 5 {
		t.Errorf("driver called %d times, want 5", d.calls.Load())
	}
	stat, ok := res.Stats[target.Traversal]
	if !ok {
		t.Fatal("no Traversal stat in result")
	}
	if stat.Count != 5 {
		t.Errorf("Traversal.Count = %d, want 5", stat.Count)
	}
	if stat.Errors != 0 {
		t.Errorf("Traversal.Errors = %d, want 0", stat.Errors)
	}
}

// TestRunWarmupExcluded proves that ops with Offset < Warmup are fired
// (driver called) but not counted in the steady-state Stats.
func TestRunWarmupExcluded(t *testing.T) {
	d := &fakeDriver{}
	// 10 ops at 1000/s: offsets 0..9ms. Warmup = 5ms means ops 0..4 are warmup.
	ops := BuildSchedule(makeOps(10, target.Traversal), 1000, 0)
	warmup := 5 * time.Millisecond
	opt := Options{Rate: 1000, Count: 10, Warmup: warmup, Concurrency: 2}

	res := Run(context.Background(), d, ops, opt)

	// All 10 ops must be fired so the engine is loaded.
	if d.calls.Load() != 10 {
		t.Errorf("driver called %d times, want 10 (warmup + steady)", d.calls.Load())
	}
	stat := res.Stats[target.Traversal]
	// Ops at offsets 0..4ms are warmup (< 5ms); ops 5..9ms are steady.
	// Exactly 5 steady samples expected.
	if stat.Count != 5 {
		t.Errorf("Traversal.Count = %d, want 5 (steady only)", stat.Count)
	}
}

// TestRunErrorsCounted proves that a driver error increments Errors in the Stat
// and is excluded from the latency percentiles, keeping the tail honest.
func TestRunErrorsCounted(t *testing.T) {
	d := &fakeDriver{err: errors.New("engine down")}
	ops := BuildSchedule(makeOps(4, target.Subgraph), 1000, 0)
	opt := Options{Rate: 1000, Count: 4, Concurrency: 1}

	res := Run(context.Background(), d, ops, opt)

	stat := res.Stats[target.Subgraph]
	if stat.Count != 4 {
		t.Errorf("Count = %d, want 4", stat.Count)
	}
	if stat.Errors != 4 {
		t.Errorf("Errors = %d, want 4", stat.Errors)
	}
	// No successful samples: all percentiles should be zero.
	if stat.P99 != 0 {
		t.Errorf("P99 = %v with all errors, want 0", stat.P99)
	}
}

// TestRunMultiClass proves that Stats are keyed per class when ops carry
// different classes.
func TestRunMultiClass(t *testing.T) {
	d := &fakeDriver{}
	ops := []Op{
		{Class: target.PointRead},
		{Class: target.Traversal},
		{Class: target.PointRead},
	}
	ops = BuildSchedule(ops, 1000, 0)
	opt := Options{Rate: 1000, Count: 3, Concurrency: 1}

	res := Run(context.Background(), d, ops, opt)

	if res.Stats[target.PointRead].Count != 2 {
		t.Errorf("PointRead.Count = %d, want 2", res.Stats[target.PointRead].Count)
	}
	if res.Stats[target.Traversal].Count != 1 {
		t.Errorf("Traversal.Count = %d, want 1", res.Stats[target.Traversal].Count)
	}
}

// TestRunContextCancel proves that cancelling the context stops dispatching new
// ops and that the function still returns (drain settles in-flight goroutines).
func TestRunContextCancel(t *testing.T) {
	d := &fakeDriver{latency: 2 * time.Millisecond}
	// 100 ops at 50/s: the run would take 2 seconds at full rate.
	ops := BuildSchedule(makeOps(100, target.Traversal), 50, 0)
	opt := Options{Rate: 50, Count: 100, Concurrency: 4}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	res := Run(ctx, d, ops, opt)

	// Fewer than 100 ops should have been fired.
	called := d.calls.Load()
	if called >= 100 {
		t.Errorf("expected cancellation to stop dispatch early, but driver called %d times", called)
	}
	// The result is valid even on early exit (may be empty).
	_ = res
}

// TestRunCountModeIsServiceTime is the regression test for the measurement bug:
// in count mode (no offered rate) the worker pool serializes ops, and timing
// from the shared start would report each op's queue position, so p50 would
// scale with the op count. With the dispatch-timed fix, p50 is the engine's
// per-query service time and does not grow with --count. The driver sleeps a
// fixed 2ms per call; whether 10 or 200 ops run, p50 must stay near 2ms.
func TestRunCountModeIsServiceTime(t *testing.T) {
	const perCall = 2 * time.Millisecond

	run := func(n int) Stat {
		d := &fakeDriver{latency: perCall}
		// Count mode: no rate, default concurrency -> pool of 1, offsets all 0.
		ops := makeOps(n, target.Traversal)
		res := Run(context.Background(), d, ops, Options{Count: n})
		if res.Latency != ServiceTimeLatency {
			t.Fatalf("n=%d: Latency model = %q, want %q", n, res.Latency, ServiceTimeLatency)
		}
		return res.Stats[target.Traversal]
	}

	small := run(10)
	large := run(200)

	// Service time, not queue depth: p50 stays near the per-call latency even as
	// the op count grows 20x. The pre-fix bug would put large.P50 near
	// (200/2)*2ms = 200ms; a generous 10x-per-call ceiling still catches it.
	ceiling := 10 * perCall
	if large.P50 > ceiling {
		t.Errorf("count=200 p50 = %v, want <= %v (queue depth leaking into latency)", large.P50, ceiling)
	}
	// And it must not scale with count: doubling-and-then-some, not 20x.
	if small.P50 > 0 && large.P50 > 3*small.P50 {
		t.Errorf("p50 scaled with count: count=10 p50=%v, count=200 p50=%v (ratio %.1fx)",
			small.P50, large.P50, float64(large.P50)/float64(small.P50))
	}
}

// TestRunRateModeIsOpenModel proves a rate-limited run stamps the open-model
// clock, the complement of the count-mode service-time stamp above.
func TestRunRateModeIsOpenModel(t *testing.T) {
	d := &fakeDriver{}
	ops := BuildSchedule(makeOps(5, target.Traversal), 1000, 0)
	res := Run(context.Background(), d, ops, Options{Rate: 1000, Count: 5, Concurrency: 1})
	if res.Latency != OpenModelLatency {
		t.Errorf("Latency model = %q, want %q", res.Latency, OpenModelLatency)
	}
}

// TestBuildScheduleOffsets checks that BuildSchedule assigns evenly-spaced
// offsets based on the rate.
func TestBuildScheduleOffsets(t *testing.T) {
	ops := makeOps(4, target.Traversal)
	BuildSchedule(ops, 100, 0) // 100 q/s -> 10ms interval

	want := []time.Duration{0, 10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond}
	for i, op := range ops {
		if op.Offset != want[i] {
			t.Errorf("ops[%d].Offset = %v, want %v", i, op.Offset, want[i])
		}
	}
}

// TestBuildScheduleZeroRate proves that a zero rate is a no-op (no panic).
func TestBuildScheduleZeroRate(t *testing.T) {
	ops := makeOps(3, target.Write)
	out := BuildSchedule(ops, 0, 0)
	for _, op := range out {
		if op.Offset != 0 {
			t.Errorf("zero-rate offset non-zero: %v", op.Offset)
		}
	}
}

// TestOptionsWindow checks the window calculation from Count, Rate, and Warmup.
func TestOptionsWindow(t *testing.T) {
	// Count=10 at Rate=100/s: total=100ms; Warmup=20ms; window=80ms.
	opt := Options{Rate: 100, Count: 10, Warmup: 20 * time.Millisecond}
	got := opt.window()
	want := 80 * time.Millisecond
	if got != want {
		t.Errorf("window = %v, want %v", got, want)
	}
}

// TestOptionsWindowDuration checks the window calculation from Duration.
func TestOptionsWindowDuration(t *testing.T) {
	opt := Options{Duration: 500 * time.Millisecond, Warmup: 100 * time.Millisecond}
	got := opt.window()
	want := 400 * time.Millisecond
	if got != want {
		t.Errorf("window = %v, want %v", got, want)
	}
}

// TestOptionsTimeout proves the default timeout is 60 seconds.
func TestOptionsTimeout(t *testing.T) {
	opt := Options{}
	if opt.timeout() != 60*time.Second {
		t.Errorf("default timeout = %v, want 60s", opt.timeout())
	}
	opt.Timeout = 5 * time.Second
	if opt.timeout() != 5*time.Second {
		t.Errorf("explicit timeout = %v, want 5s", opt.timeout())
	}
}

// TestDrainAndClose proves drainAndClose returns the row count and does not
// return an error (the error is in res.Err() which drainAndClose ignores).
func TestDrainAndClose(t *testing.T) {
	res := &fakeResult{n: 7}
	n := drainAndClose(res)
	if n != 7 {
		t.Errorf("drainAndClose = %d, want 7", n)
	}
}
