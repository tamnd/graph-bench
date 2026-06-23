package gate_test

import (
	"context"
	"testing"
	"time"

	gradapter "github.com/tamnd/graph-bench/adapter/gr"
	"github.com/tamnd/graph-bench/gate"
	"github.com/tamnd/graph-bench/measure"
	"github.com/tamnd/graph-bench/slo"
	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
	_ "github.com/tamnd/graph-bench/workload/micro" // register micro workloads
)

// TestSmokeGate runs gr in-process on the bounded micro-grid workload and
// asserts every bounded class holds its budget. No container, no giant dataset:
// the workload generates a synthetic grid internally, so this test fits the
// 2-vCPU CI runner and runs on every change.
//
// The empty-verdict guard is critical: an empty report passes vacuously, so
// the count check turns a gate that measured nothing into a failure rather
// than a silent green.
func TestSmokeGate(t *testing.T) {
	wl := workload.Bounded()
	if wl == nil {
		t.Fatal("workload.Bounded() returned nil; import _ workload/micro is missing")
	}

	spec := gate.Spec{
		Engines:  []target.Target{gradapter.New()},
		Workload: wl,
	}
	out, err := gate.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("smoke gate run: %v", err)
	}
	if n := len(out.Report.Verdicts); n == 0 {
		t.Fatal("smoke gate measured no bounded class: a gate that gated nothing is a broken gate, not a pass")
	}
	for _, v := range out.Report.Failures() {
		t.Errorf("smoke gate: %s", v.Reason)
	}
}

// TestOutcomePassAll checks that an Outcome with a passing Report and an empty
// Flatness report passes overall.
func TestOutcomePassAll(t *testing.T) {
	out := gate.Outcome{
		Report:   slo.Report{Verdicts: []slo.Verdict{{Class: target.Traversal, Pass: true}}},
		Flatness: slo.FlatnessReport{},
	}
	if !out.Pass() {
		t.Error("all-passing Outcome should return Pass() = true")
	}
}

// TestOutcomeFailOnBudget proves that a budget failure makes Pass() false.
func TestOutcomeFailOnBudget(t *testing.T) {
	out := gate.Outcome{
		Report: slo.Report{Verdicts: []slo.Verdict{
			{Class: target.Traversal, Pass: false, Reason: "p99 too high"},
		}},
	}
	if out.Pass() {
		t.Error("failed budget verdict should make Pass() = false")
	}
}

// TestOutcomeFailOnFlatness proves that a flatness failure makes Pass() false
// even when the budget passes.
func TestOutcomeFailOnFlatness(t *testing.T) {
	out := gate.Outcome{
		Report: slo.Report{Verdicts: []slo.Verdict{{Class: target.Traversal, Pass: true}}},
		Flatness: slo.FlatnessReport{Verdicts: []slo.FlatnessVerdict{
			{Class: target.Traversal, Pass: false, Reason: "ratio too high"},
		}},
	}
	if out.Pass() {
		t.Error("flatness failure should make Pass() = false")
	}
}

// TestSpecWithDefaults proves that a zero Spec gets usable defaults.
func TestSpecWithDefaults(t *testing.T) {
	// Exercise gate.Run with explicit zero fields other than the required ones to
	// confirm defaults fill in without panic; we can't call withDefaults directly
	// (unexported), but a Run call exercises the same path.
	wl := workload.Bounded()
	if wl == nil {
		t.Skip("workload.Bounded() nil; micro not imported")
	}
	// We run with zero Concurrency, Rate, etc. to verify defaults kick in.
	spec := gate.Spec{
		Engines:  []target.Target{gradapter.New()},
		Workload: wl,
		// Rate: 0 -> defaults to 200
		// Concurrency: nil -> defaults to [1]
	}
	out, err := gate.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run with zero-field Spec: %v", err)
	}
	if out.Results == nil {
		t.Error("Run returned nil Results")
	}
}

// TestSmokeGateWrongAnswer simulates the F1 rule: a validation failure before
// timing means the gate catches a wrong answer, not just a slow engine. This
// test verifies the slo.Check path by injecting an obviously-failing result.
func TestSmokeGateWrongAnswer(t *testing.T) {
	// Build a result where the error count is non-zero (simulating a query that
	// returned a wrong answer and was counted as an error by F1 enforcement).
	res := measure.Result{
		Stats: map[target.Class]measure.Stat{
			target.Traversal: {Class: target.Traversal, Count: 10, Errors: 10, P99: 0},
		},
	}
	rep := slo.Check(res)
	if rep.Pass() {
		t.Error("all-error result should fail the gate (F1: wrong answers are errors, not latencies)")
	}
}

// TestSmokeGateDeliberateSlowdown proves that an injected p99 above the ceiling
// fails the gate. This confirms the gate gates rather than rubber-stamps.
func TestSmokeGateDeliberateSlowdown(t *testing.T) {
	res := measure.Result{
		Stats: map[target.Class]measure.Stat{
			target.PointRead: {
				Class:  target.PointRead,
				Count:  100,
				Errors: 0,
				P99:    100 * time.Millisecond, // far over the 500µs ceiling
			},
		},
	}
	rep := slo.Check(res)
	if rep.Pass() {
		t.Error("100ms p99 on PointRead should fail the 500µs ceiling")
	}
	if len(rep.Failures()) == 0 {
		t.Error("expected at least one failure reason")
	}
}
