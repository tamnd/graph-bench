package measure

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// TestRunAllSteady checks that Run with no warmup records every op as a steady
// sample and produces a Stat with the right Count.
func TestRunAllSteady(t *testing.T) {
	s := &fakeSession{}
	ops := BuildSchedule(makeOps(5, engine.Traversal), 1000, 0)
	opt := Options{Rate: 1000, Count: 5, Concurrency: 1}

	res := Run(context.Background(), s, ops, opt)

	if s.calls.Load() != 5 {
		t.Errorf("session called %d times, want 5", s.calls.Load())
	}
	stat, ok := res.Stats[engine.Traversal]
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
// (session called) but not counted in the steady-state Stats (spec 08 §3:
// warmup samples are discarded).
func TestRunWarmupExcluded(t *testing.T) {
	s := &fakeSession{}
	// 10 ops at 1000/s: offsets 0..9ms. Warmup = 5ms means ops 0..4 are warmup.
	ops := BuildSchedule(makeOps(10, engine.Traversal), 1000, 0)
	warmup := 5 * time.Millisecond
	opt := Options{Rate: 1000, Count: 10, Warmup: warmup, Concurrency: 2}

	res := Run(context.Background(), s, ops, opt)

	// All 10 ops must be fired so the engine is loaded.
	if s.calls.Load() != 10 {
		t.Errorf("session called %d times, want 10 (warmup + steady)", s.calls.Load())
	}
	stat := res.Stats[engine.Traversal]
	// Ops at offsets 0..4ms are warmup (< 5ms); ops 5..9ms are steady.
	// Exactly 5 steady samples expected.
	if stat.Count != 5 {
		t.Errorf("Traversal.Count = %d, want 5 (steady only)", stat.Count)
	}
}

// TestRunErrorsCounted proves that a session error increments Errors in the
// Stat and is excluded from the latency percentiles, keeping the tail honest
// (F10: errors counted in Count, excluded from the latency array).
func TestRunErrorsCounted(t *testing.T) {
	s := &fakeSession{err: errors.New("engine down")}
	ops := BuildSchedule(makeOps(4, engine.Subgraph), 1000, 0)
	opt := Options{Rate: 1000, Count: 4, Concurrency: 1}

	res := Run(context.Background(), s, ops, opt)

	stat := res.Stats[engine.Subgraph]
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
	s := &fakeSession{}
	ops := []Op{
		{Op: engine.Op{Class: engine.PointRead}},
		{Op: engine.Op{Class: engine.Traversal}},
		{Op: engine.Op{Class: engine.PointRead}},
	}
	ops = BuildSchedule(ops, 1000, 0)
	opt := Options{Rate: 1000, Count: 3, Concurrency: 1}

	res := Run(context.Background(), s, ops, opt)

	if res.Stats[engine.PointRead].Count != 2 {
		t.Errorf("PointRead.Count = %d, want 2", res.Stats[engine.PointRead].Count)
	}
	if res.Stats[engine.Traversal].Count != 1 {
		t.Errorf("Traversal.Count = %d, want 1", res.Stats[engine.Traversal].Count)
	}
}

// TestRunContextCancel proves that cancelling the context stops dispatching new
// ops and that the function still returns (drain settles in-flight goroutines).
func TestRunContextCancel(t *testing.T) {
	s := &fakeSession{latency: 2 * time.Millisecond}
	// 100 ops at 50/s: the run would take 2 seconds at full rate.
	ops := BuildSchedule(makeOps(100, engine.Traversal), 50, 0)
	opt := Options{Rate: 50, Count: 100, Concurrency: 4}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	res := Run(ctx, s, ops, opt)

	// Fewer than 100 ops should have been fired.
	called := s.calls.Load()
	if called >= 100 {
		t.Errorf("expected cancellation to stop dispatch early, but session called %d times", called)
	}
	// The result is valid even on early exit (may be empty).
	_ = res
}

// TestCountModeLatencyIndependentOfCount is the regression test for the v1
// measurement bug (spec 08 §2): in count mode (no offered rate) the worker
// pool serializes ops, and timing from the shared schedule-build start would
// report each op's queue position, so p50 would scale with --count. With the
// dispatch-timed fix, p50 is the engine's per-query service time and does not
// grow with the op count. The session sleeps a fixed 2ms per call; whether 10
// or 200 ops run, p50 must stay near 2ms.
func TestCountModeLatencyIndependentOfCount(t *testing.T) {
	const perCall = 2 * time.Millisecond

	run := func(n int) Stat {
		s := &fakeSession{latency: perCall}
		// Count mode: no rate, default concurrency -> pool of 1, offsets all 0.
		ops := makeOps(n, engine.Traversal)
		res := Run(context.Background(), s, ops, Options{Count: n})
		if res.Latency != ServiceTimeLatency {
			t.Fatalf("n=%d: Latency model = %q, want %q", n, res.Latency, ServiceTimeLatency)
		}
		return res.Stats[engine.Traversal]
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
	s := &fakeSession{}
	ops := BuildSchedule(makeOps(5, engine.Traversal), 1000, 0)
	res := Run(context.Background(), s, ops, Options{Rate: 1000, Count: 5, Concurrency: 1})
	if res.Latency != OpenModelLatency {
		t.Errorf("Latency model = %q, want %q", res.Latency, OpenModelLatency)
	}
}

// TestBuildScheduleOffsets checks that BuildSchedule assigns evenly-spaced
// offsets based on the rate.
func TestBuildScheduleOffsets(t *testing.T) {
	ops := makeOps(4, engine.Traversal)
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
	ops := makeOps(3, engine.Write)
	out := BuildSchedule(ops, 0, 0)
	for _, op := range out {
		if op.Offset != 0 {
			t.Errorf("zero-rate offset non-zero: %v", op.Offset)
		}
	}
}

// TestByQueryPopulated proves that running ops with QueryID set populates
// Result.ByQuery.
func TestByQueryPopulated(t *testing.T) {
	s := &fakeSession{}
	ops := []Op{
		{Op: engine.Op{Class: engine.PointRead, QueryID: "q-alpha", Text: "RETURN 1"}},
		{Op: engine.Op{Class: engine.PointRead, QueryID: "q-beta", Text: "RETURN 2"}},
		{Op: engine.Op{Class: engine.PointRead, QueryID: "q-alpha", Text: "RETURN 1"}},
	}
	ops = BuildSchedule(ops, 10, 0)
	res := Run(context.Background(), s, ops, Options{Concurrency: 1, Count: len(ops)})
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

// TestBudgetTruncatesPerQuery checks that a count-mode budget is spent per
// query rather than per run. The failure it guards against is the obvious
// implementation — one deadline over the whole run — which would spend the
// budget on whichever query is dispatched first and report nothing at all for
// the ones after it.
func TestBudgetTruncatesPerQuery(t *testing.T) {
	sess := &fakeSession{perOp: map[string]time.Duration{
		"slow":  ms(20),
		"quick": ms(1),
	}}
	var ops []Op
	for _, id := range []string{"slow", "quick"} {
		for i := 0; i < 200; i++ {
			ops = append(ops, Op{Op: engine.Op{QueryID: id, Class: engine.PointRead}})
		}
	}

	res := Run(context.Background(), sess, ops, Options{
		Count:       len(ops),
		Concurrency: 1,
		Budget:      ms(100),
	})

	slow, ok := res.ByQuery["slow"]
	if !ok {
		t.Fatal("no stats for slow query")
	}
	quick, ok := res.ByQuery["quick"]
	if !ok {
		t.Fatal("no stats for quick query: the budget was spent per run, not per query")
	}
	if slow.Count == 0 || slow.Count > 40 {
		t.Errorf("slow count = %d, want a truncated but non-empty sample", slow.Count)
	}
	if quick.Count <= slow.Count {
		t.Errorf("quick count = %d, slow count = %d: the cheaper query should fit more repetitions in the same budget", quick.Count, slow.Count)
	}
	if quick.Count == 200 && slow.Count == 200 {
		t.Error("nothing was truncated")
	}
}

// stateSession models the thing that makes unbracketed write repetition
// meaningless: a store with a primary key. "create" succeeds only when the
// row is absent, "delete" removes it. An engine that enforces uniqueness
// behaves exactly this way, which is why snb-iu1 errored 100 times out of 100
// on Ladybug while Neo4j, which has no such constraint, quietly accumulated
// duplicates.
type stateSession struct {
	mu      sync.Mutex
	present bool
	order   []string
	setupMs time.Duration
}

func (s *stateSession) Version(context.Context) (string, error) { return "state-1.0", nil }

func (s *stateSession) Load(context.Context, engine.Dataset) (engine.LoadStats, error) {
	return engine.LoadStats{}, nil
}

func (s *stateSession) Exec(_ context.Context, op engine.Op) (engine.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order = append(s.order, op.Text)
	switch op.Text {
	case "setup":
		time.Sleep(s.setupMs)
		return &fakeResult{}, nil
	case "create":
		if s.present {
			return nil, errors.New("duplicate primary key")
		}
		s.present = true
		return &fakeResult{}, nil
	case "delete":
		s.present = false
		return &fakeResult{}, nil
	}
	return &fakeResult{}, nil
}

func (s *stateSession) Begin(context.Context, engine.AccessMode) (engine.Tx, error) {
	return nil, errors.New("state session: no transactions")
}

func (s *stateSession) Close(context.Context) error { return nil }

// TestBracketMakesWriteRepetitionStationary is the regression test for a bug
// that made every write number in the suite meaningless: Setup and Teardown
// were run by verification only, so the measured repetitions ran against a
// graph verification had already torn down. Repetitions 2..N of a keyed
// insert then collided with repetition 1.
func TestBracketMakesWriteRepetitionStationary(t *testing.T) {
	ops := make([]Op, 20)
	for i := range ops {
		ops[i] = Op{Op: engine.Op{QueryID: "ins", Class: engine.Write, Text: "create"}}
	}

	unbracketed := &stateSession{}
	res := Run(context.Background(), unbracketed, ops, Options{Count: len(ops), Concurrency: 1})
	if got := res.ByQuery["ins"].Errors; got != len(ops)-1 {
		t.Errorf("without a bracket: Errors = %d, want %d (all but the first collide)", got, len(ops)-1)
	}

	bracketed := &stateSession{}
	res = Run(context.Background(), bracketed, ops, Options{
		Count: len(ops), Concurrency: 1,
		Brackets: map[string]Bracket{"ins": {Teardown: "delete"}},
	})
	if got := res.ByQuery["ins"].Errors; got != 0 {
		t.Errorf("with a bracket: Errors = %d, want 0", got)
	}
	if got := res.ByQuery["ins"].Count; got != len(ops) {
		t.Errorf("Count = %d, want %d", got, len(ops))
	}
}

// TestBracketNotTimed proves the staging statement stays outside the clock:
// the harness's cost of making a repetition stationary is not the engine's
// cost of doing the write.
func TestBracketNotTimed(t *testing.T) {
	ops := make([]Op, 4)
	for i := range ops {
		ops[i] = Op{Op: engine.Op{QueryID: "ins", Class: engine.Write, Text: "create"}}
	}
	sess := &stateSession{setupMs: 20 * time.Millisecond}
	res := Run(context.Background(), sess, ops, Options{
		Count: len(ops), Concurrency: 1,
		Brackets: map[string]Bracket{"ins": {Setup: "setup", Teardown: "delete"}},
	})
	if got := res.ByQuery["ins"].P50; got >= 20*time.Millisecond {
		t.Errorf("P50 = %v; the 20ms setup leaked into the measured window", got)
	}
	// setup, create, delete, per repetition, in that order.
	want := []string{"setup", "create", "delete"}
	if len(sess.order) < len(want) {
		t.Fatalf("statement order too short: %v", sess.order)
	}
	for i, w := range want {
		if sess.order[i] != w {
			t.Fatalf("statement order = %v, want prefix %v", sess.order[:len(want)], want)
		}
	}
}
