package gate

import (
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
