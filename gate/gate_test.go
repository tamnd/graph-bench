package gate

import (
	"strings"
	"testing"
	"time"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/measure"
	"github.com/tamnd/graph-bench/verify"
	"github.com/tamnd/graph-bench/workload"
)

func stat(class engine.Class, p50, p99 time.Duration) measure.Stat {
	return measure.Stat{Class: class, Count: 100, P50: p50, P99: p99}
}

func TestBudgetFor(t *testing.T) {
	if d, ok := BudgetFor(engine.PointRead, engine.InProc); !ok || d != 500*time.Microsecond {
		t.Errorf("PointRead/InProc = %v, %v", d, ok)
	}
	if _, ok := BudgetFor(engine.Analytical, engine.InProc); ok {
		t.Error("Analytical must be unbudgeted (tracked, not gated)")
	}
	if _, ok := BudgetFor(engine.PointRead, engine.Native); ok {
		t.Error("Native plane has no budget table yet")
	}
}

func TestCheckBudgets(t *testing.T) {
	res := measure.Result{Stats: map[engine.Class]measure.Stat{
		engine.PointRead: stat(engine.PointRead, 100*time.Microsecond, 400*time.Microsecond),
		engine.Traversal: stat(engine.Traversal, 2*time.Millisecond, 8*time.Millisecond),
	}}
	if v := CheckBudgets(res, engine.InProc, "micro-read"); len(v) != 1 {
		t.Fatalf("violations = %v, want 1 (traversal over 5ms)", v)
	} else if v[0].Where != string(engine.Traversal) {
		t.Errorf("violation on %s, want traversal", v[0].Where)
	}
	// The same numbers pass on the looser subprocess ceilings.
	if v := CheckBudgets(res, engine.Subprocess, "micro-read"); len(v) != 0 {
		t.Errorf("subprocess violations = %v, want none", v)
	}
}

func TestFinBenchSLO(t *testing.T) {
	res := measure.Result{ByQuery: map[string]measure.Stat{
		"fb1": stat(engine.Traversal, 20*time.Millisecond, 150*time.Millisecond),
		"fb2": stat(engine.Traversal, 5*time.Millisecond, 50*time.Millisecond),
		"fbw": stat(engine.Write, time.Millisecond, 200*time.Millisecond),
	}}
	v := CheckBudgets(res, engine.Bolt, "fb-read")
	if len(v) != 1 || v[0].Where != "fb1" {
		t.Errorf("violations = %v, want exactly fb1 over the 100ms SLO (writes exempt)", v)
	}
}

func TestCheckRegression(t *testing.T) {
	now := measure.Result{ByQuery: map[string]measure.Stat{
		"q1": stat(engine.PointRead, 120*time.Microsecond, 500*time.Microsecond),
		"q2": stat(engine.PointRead, 100*time.Microsecond, 400*time.Microsecond),
		"q3": stat(engine.PointRead, time.Millisecond, time.Millisecond), // new: no baseline
	}}
	base := measure.Result{ByQuery: map[string]measure.Stat{
		"q1": stat(engine.PointRead, 100*time.Microsecond, 450*time.Microsecond),
		"q2": stat(engine.PointRead, 99*time.Microsecond, 390*time.Microsecond),
	}}
	v := CheckRegression(now, base, Options{})
	if len(v) != 2 {
		t.Fatalf("violations = %v, want q1 p50 and q1 p99", v)
	}
	for _, viol := range v {
		if viol.Where != "q1" {
			t.Errorf("violation on %s, want q1", viol.Where)
		}
	}
}

func TestCheckFlatness(t *testing.T) {
	small := measure.Result{Stats: map[engine.Class]measure.Stat{
		engine.PointRead: stat(engine.PointRead, 100*time.Microsecond, 0),
	}}
	flat := measure.Result{Stats: map[engine.Class]measure.Stat{
		engine.PointRead: stat(engine.PointRead, 150*time.Microsecond, 0),
	}}
	steep := measure.Result{Stats: map[engine.Class]measure.Stat{
		engine.PointRead: stat(engine.PointRead, 500*time.Microsecond, 0),
	}}
	if v := CheckFlatness(small, flat, Options{}); len(v) != 0 {
		t.Errorf("1.5x growth flagged: %v", v)
	}
	if v := CheckFlatness(small, steep, Options{}); len(v) != 1 {
		t.Errorf("5x growth not flagged")
	}
}

func TestCheckVerificationAndExitCodes(t *testing.T) {
	plan := &verify.Plan{
		Workload: &workload.Workload{Name: "w"},
		Reports: []verify.QueryReport{
			{QueryID: "ok", Outcome: verify.Pass},
			{QueryID: "broken", Outcome: verify.Fail, Reason: "mismatch"},
			{QueryID: "was-pass", Outcome: verify.Skip, Reason: "no-dialect-text"},
			{QueryID: "always-skip", Outcome: verify.Skip, Reason: "no-dialect-text"},
		},
	}
	v := CheckVerification(plan, map[string]bool{"ok": true, "was-pass": true})
	if len(v) != 2 {
		t.Fatalf("violations = %v, want FAIL + coverage regression", v)
	}

	d := Decision{Violations: v}
	if d.ExitCode() != ExitVerification {
		t.Errorf("exit = %d, want %d", d.ExitCode(), ExitVerification)
	}
	if (Decision{Violations: []Violation{{Kind: "budget"}}}).ExitCode() != ExitBudget {
		t.Error("budget-only decision must exit 2")
	}
	if !(Decision{}).Pass() || (Decision{}).ExitCode() != ExitPass {
		t.Error("empty decision must pass with exit 0")
	}
}

// TestWriteCeilingFollowsTheDisk covers the rule that a durable commit
// costs a sync and no engine goes below one, so the write ceiling is the
// table's number or the disk's, whichever is higher.
func TestWriteCeilingFollowsTheDisk(t *testing.T) {
	table, _ := BudgetFor(engine.Write, engine.InProc)

	// A disk fast enough for the table leaves it alone, and so does a
	// run that never probed one.
	for _, sync := range []int64{-1, 0, int64(100 * time.Microsecond)} {
		if got, raised := WriteCeiling(engine.InProc, sync, 8); got != table || raised {
			t.Errorf("WriteCeiling(%d) = %v, %v; want the table's %v", sync, got, raised, table)
		}
	}
	// A disk whose sync is slower than the whole budget sets it instead.
	slow := int64(3 * time.Millisecond)
	got, raised := WriteCeiling(engine.InProc, slow, 8)
	if want := time.Duration(DurableWriteSyncs(8)) * 3 * time.Millisecond; got != want || !raised {
		t.Errorf("WriteCeiling(slow) = %v, %v; want %v, true", got, raised, want)
	}
	// More clients on one log means more waiting, so a busier run gets
	// a higher ceiling than a quieter one on the same disk.
	quiet, _ := WriteCeiling(engine.InProc, slow, 1)
	if quiet >= got {
		t.Errorf("ceiling at 1 client (%v) is not below the ceiling at 8 (%v)", quiet, got)
	}
	// A run that did not say how many clients it had is read as one,
	// and one client still allows two syncs: a write can arrive one
	// instruction after a flush began.
	if DurableWriteSyncs(0) != 2 || DurableWriteSyncs(1) != 2 {
		t.Errorf("an unstated or single client allows %d and %d syncs, want 2",
			DurableWriteSyncs(0), DurableWriteSyncs(1))
	}
	if _, ok := BudgetFor(engine.Write, engine.Native); ok {
		t.Error("the native plane has no write budget to calibrate")
	}
	if got, raised := WriteCeiling(engine.Native, slow, 8); got != 0 || raised {
		t.Errorf("WriteCeiling on an unbudgeted plane = %v, %v; want 0, false", got, raised)
	}
}

// TestCheckBudgetsCalibratesWrites proves the gate reads that ceiling: the
// same 5ms write p99 fails on a fast disk and passes on a disk where one
// durable sync costs 3ms, because on that disk 5ms is two syncs of work
// and there is no faster correct answer.
func TestCheckBudgetsCalibratesWrites(t *testing.T) {
	res := func(sync int64) measure.Result {
		return measure.Result{
			Stats: map[engine.Class]measure.Stat{
				engine.Write: stat(engine.Write, 4*time.Millisecond, 5*time.Millisecond),
			},
			Condition: measure.Condition{
				Concurrency: []int{1},
				Hardware:    measure.Hardware{SyncNanos: sync},
			},
		}
	}
	v := CheckBudgets(res(int64(100*time.Microsecond)), engine.InProc, "snb-update")
	if len(v) != 1 || v[0].Where != string(engine.Write) {
		t.Fatalf("on a fast disk 5ms must fail the 2ms ceiling, got %v", v)
	}
	if v := CheckBudgets(res(int64(3*time.Millisecond)), engine.InProc, "snb-update"); len(v) != 0 {
		t.Errorf("on a 3ms sync 5ms is inside two syncs, got %v", v)
	}
	// And a run that still fails the raised ceiling says whose ceiling
	// it was, so a reader is never left thinking the spec table held.
	slow := res(int64(3 * time.Millisecond))
	slow.Stats[engine.Write] = stat(engine.Write, 4*time.Millisecond, 30*time.Millisecond)
	v = CheckBudgets(slow, engine.InProc, "snb-update")
	if len(v) != 1 {
		t.Fatalf("30ms must fail even the raised ceiling, got %v", v)
	}
	if !strings.Contains(v[0].Detail, "durable syncs") {
		t.Errorf("the detail does not say the disk set the ceiling: %q", v[0].Detail)
	}
}

// TestCheckBudgetsReadsTheRunsConcurrency proves the ceiling moves with the
// number of clients the run had. Durable commits through one log are
// serialized, so eight clients means a write can be waiting on seven others
// before its own flush, and the same latency that is a queue at eight
// clients is the engine being slow at one.
func TestCheckBudgetsReadsTheRunsConcurrency(t *testing.T) {
	res := func(clients int) measure.Result {
		return measure.Result{
			Stats: map[engine.Class]measure.Stat{
				engine.Write: stat(engine.Write, 6*time.Millisecond, 18*time.Millisecond),
			},
			Condition: measure.Condition{
				Concurrency: []int{clients},
				Hardware:    measure.Hardware{SyncNanos: int64(3 * time.Millisecond)},
			},
		}
	}
	if v := CheckBudgets(res(8), engine.InProc, "snb-mix"); len(v) != 0 {
		t.Errorf("18ms at eight clients is inside the queue they make, got %v", v)
	}
	v := CheckBudgets(res(1), engine.InProc, "snb-mix")
	if len(v) != 1 {
		t.Fatalf("18ms alone on the log is six syncs of work with nothing to wait for, got %v", v)
	}
	if !strings.Contains(v[0].Detail, "at a concurrency of 1") {
		t.Errorf("the detail does not name the concurrency it allowed for: %q", v[0].Detail)
	}
	// A sweep publishes one set of numbers for every point it ran, so
	// the ceiling has to allow for the busiest of them.
	sweep := res(1)
	sweep.Condition.Concurrency = []int{1, 4, 8}
	if v := CheckBudgets(sweep, engine.InProc, "snb-mix"); len(v) != 0 {
		t.Errorf("a sweep is gated at its busiest point, got %v", v)
	}
}

// TestCheckDriftFailsADegradingRun proves the gate reads what a sustained run
// did over its own length, not just the one p99 it published. A run that is
// fast for its first window and slower for the rest reports a fine p99 over
// the whole thing, because the fast part is averaged in.
func TestCheckDriftFailsADegradingRun(t *testing.T) {
	res := func(first, worst time.Duration) measure.Result {
		return measure.Result{
			Drift: map[engine.Class]measure.Drift{
				engine.Write: {
					Window:  10 * time.Second,
					Windows: 6,
					First:   measure.Stat{P99: first},
					Worst:   measure.Stat{P99: worst},
					WorstAt: 50 * time.Second,
					Trend:   float64(worst) / float64(first),
				},
			},
		}
	}
	if v := CheckDrift(res(10*time.Millisecond, 10*time.Millisecond), Options{}); len(v) != 0 {
		t.Errorf("a run that ended the way it started is not drift, got %v", v)
	}
	if v := CheckDrift(res(10*time.Millisecond, 11*time.Millisecond), Options{}); len(v) != 0 {
		t.Errorf("a tenth is inside the allowed factor, got %v", v)
	}
	v := CheckDrift(res(10*time.Millisecond, 40*time.Millisecond), Options{})
	if len(v) != 1 || v[0].Kind != "drift" || v[0].Where != string(engine.Write) {
		t.Fatalf("four times slower by the end must fail, got %v", v)
	}
	if !strings.Contains(v[0].Detail, "4.00x") || !strings.Contains(v[0].Detail, "10ms in the first") {
		t.Errorf("the detail does not carry the trend and what it started at: %q", v[0].Detail)
	}
	// A caller who says what they will accept gets that instead.
	if v := CheckDrift(res(10*time.Millisecond, 40*time.Millisecond), Options{DriftFactor: 5.0}); len(v) != 0 {
		t.Errorf("4x is inside a 5x factor, got %v", v)
	}
}

// TestCheckDriftIgnoresAnalyticsAndShortRuns proves the two cases that decide
// nothing: analytics is tracked and never gated, and a run too short to hold
// two windows records no drift to read.
func TestCheckDriftIgnoresAnalyticsAndShortRuns(t *testing.T) {
	analytics := measure.Result{
		Drift: map[engine.Class]measure.Drift{
			engine.Analytical: {Window: 10 * time.Second, Windows: 6, Trend: 9.0},
		},
	}
	if v := CheckDrift(analytics, Options{}); len(v) != 0 {
		t.Errorf("analytics is tracked, never gated, got %v", v)
	}
	if v := CheckDrift(measure.Result{}, Options{}); len(v) != 0 {
		t.Errorf("a run with no drift recorded decides nothing, got %v", v)
	}
}
