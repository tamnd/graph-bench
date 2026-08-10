package measure

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// analyticsOps builds n analytical engine.Ops with distinct query ids.
func analyticsOps(ids ...string) []engine.Op {
	ops := make([]engine.Op, len(ids))
	for i, id := range ids {
		ops[i] = engine.Op{QueryID: id, Class: engine.Analytical, Text: "CALL kernel()"}
	}
	return ops
}

// TestRunAnalyticsDiscardFirst proves the warm-mode protocol (spec 07 §1,
// 08 §4): every repetition executes, but the first is discarded from the kept
// per-rep durations and the summary.
func TestRunAnalyticsDiscardFirst(t *testing.T) {
	s := &fakeSession{}
	ops := analyticsOps("ga-bfs", "ga-pr")

	res, err := RunAnalytics(context.Background(), s, ops, 4, true)
	if err != nil {
		t.Fatalf("RunAnalytics: %v", err)
	}

	// 2 queries x 4 reps = 8 Exec calls (the discarded rep still runs).
	if s.calls.Load() != 8 {
		t.Errorf("session called %d times, want 8", s.calls.Load())
	}
	for _, id := range []string{"ga-bfs", "ga-pr"} {
		if got := len(res.PerQuery[id]); got != 3 {
			t.Errorf("PerQuery[%q] kept %d reps, want 3 (first discarded)", id, got)
		}
		if res.Stats[id].Count != 3 {
			t.Errorf("Stats[%q].Count = %d, want 3", id, res.Stats[id].Count)
		}
		if res.Stats[id].Class != engine.Analytical {
			t.Errorf("Stats[%q].Class = %v, want Analytical", id, res.Stats[id].Class)
		}
	}
}

// TestRunAnalyticsKeepAll proves cold mode (discardFirst=false) keeps every
// repetition.
func TestRunAnalyticsKeepAll(t *testing.T) {
	s := &fakeSession{}
	ops := analyticsOps("gap-tc")

	res, err := RunAnalytics(context.Background(), s, ops, 3, false)
	if err != nil {
		t.Fatalf("RunAnalytics: %v", err)
	}
	if got := len(res.PerQuery["gap-tc"]); got != 3 {
		t.Errorf("kept %d reps, want 3", got)
	}
	if res.Stats["gap-tc"].Count != 3 {
		t.Errorf("Stats.Count = %d, want 3", res.Stats["gap-tc"].Count)
	}
}

// TestRunAnalyticsDefaultReps proves reps <= 0 defaults to the spec default
// of 5 repetitions.
func TestRunAnalyticsDefaultReps(t *testing.T) {
	s := &fakeSession{}
	ops := analyticsOps("ga-wcc")

	res, err := RunAnalytics(context.Background(), s, ops, 0, false)
	if err != nil {
		t.Fatalf("RunAnalytics: %v", err)
	}
	if s.calls.Load() != 5 {
		t.Errorf("session called %d times, want 5 (default reps)", s.calls.Load())
	}
	if got := len(res.PerQuery["ga-wcc"]); got != 5 {
		t.Errorf("kept %d reps, want 5", got)
	}
}

// TestRunAnalyticsSingleStream proves repetitions run one at a time: with a
// fixed per-Exec latency the total wall time is at least reps x latency.
func TestRunAnalyticsSingleStream(t *testing.T) {
	s := &fakeSession{latency: 5 * time.Millisecond}
	ops := analyticsOps("ga-sssp")

	start := time.Now()
	if _, err := RunAnalytics(context.Background(), s, ops, 3, false); err != nil {
		t.Fatalf("RunAnalytics: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Errorf("elapsed %v looks concurrent (want >= 15ms for 3 x 5ms serial reps)", elapsed)
	}
}

// TestRunAnalyticsError proves an Exec failure aborts the run and surfaces the
// query id — an analytical kernel failing is news, not a tail sample.
func TestRunAnalyticsError(t *testing.T) {
	s := &fakeSession{err: errors.New("kernel not found")}
	ops := analyticsOps("ga-cdlp")

	_, err := RunAnalytics(context.Background(), s, ops, 3, true)
	if err == nil {
		t.Fatal("expected error from failing session")
	}
	if !errors.Is(err, s.err) {
		t.Errorf("error %v does not wrap the session error", err)
	}
}

// TestRunAnalyticsContextCancel proves a cancelled context stops the run with
// an error instead of hanging through the remaining repetitions.
func TestRunAnalyticsContextCancel(t *testing.T) {
	s := &fakeSession{latency: 10 * time.Millisecond}
	ops := analyticsOps("ga-lcc")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()

	_, err := RunAnalytics(ctx, s, ops, 20, false)
	if err == nil {
		t.Fatal("expected context error")
	}
	if s.calls.Load() >= 20 {
		t.Errorf("session called %d times despite cancel", s.calls.Load())
	}
}
