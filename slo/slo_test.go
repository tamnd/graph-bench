package slo_test

import (
	"testing"
	"time"

	"github.com/tamnd/graph-bench/measure"
	"github.com/tamnd/graph-bench/slo"
	"github.com/tamnd/graph-bench/target"
)

// makeStat builds a measure.Stat for testing.
func makeStat(class target.Class, p99 time.Duration, count, errors int) measure.Stat {
	return measure.Stat{
		Class:  class,
		Count:  count,
		Errors: errors,
		P99:    p99,
	}
}

// makeResult builds a measure.Result with the given Stats map.
func makeResult(stats map[target.Class]measure.Stat) measure.Result {
	return measure.Result{Stats: stats}
}

// TestCheckPassesUnderCeiling proves a bounded class under its ceiling passes.
func TestCheckPassesUnderCeiling(t *testing.T) {
	res := makeResult(map[target.Class]measure.Stat{
		target.Traversal: makeStat(target.Traversal, 2*time.Millisecond, 100, 0),
	})
	rep := slo.Check(res)
	if !rep.Pass() {
		t.Errorf("want pass, got failures: %v", rep.Failures())
	}
	if len(rep.Verdicts) != 1 {
		t.Errorf("expected 1 verdict, got %d", len(rep.Verdicts))
	}
}

// TestCheckFailsOverCeiling proves a p99 over the ceiling fails the verdict.
func TestCheckFailsOverCeiling(t *testing.T) {
	res := makeResult(map[target.Class]measure.Stat{
		target.PointRead: makeStat(target.PointRead, 10*time.Millisecond, 100, 0), // over 500µs ceiling
	})
	rep := slo.Check(res)
	if rep.Pass() {
		t.Error("want fail, got pass; p99 over ceiling should fail")
	}
	if len(rep.Failures()) == 0 {
		t.Error("expected at least one failure")
	}
}

// TestCheckFailsOnErrors proves that any transport errors fail the class
// regardless of latency.
func TestCheckFailsOnErrors(t *testing.T) {
	res := makeResult(map[target.Class]measure.Stat{
		target.Write: makeStat(target.Write, 1*time.Millisecond, 100, 1), // 1 error, p99 under ceiling
	})
	rep := slo.Check(res)
	if rep.Pass() {
		t.Error("want fail: any transport errors should fail the class")
	}
}

// TestCheckSkipsAnalytical proves the analytical class is not checked against
// a latency ceiling (it has no ceiling and must not appear in the verdicts).
func TestCheckSkipsAnalytical(t *testing.T) {
	res := makeResult(map[target.Class]measure.Stat{
		target.Analytical: makeStat(target.Analytical, 30*time.Second, 10, 0),
	})
	rep := slo.Check(res)
	if len(rep.Verdicts) != 0 {
		t.Errorf("analytical class should not produce a verdict, got %v", rep.Verdicts)
	}
}

// TestCheckSkipsZeroCount proves a class with no samples is skipped.
func TestCheckSkipsZeroCount(t *testing.T) {
	res := makeResult(map[target.Class]measure.Stat{
		target.Traversal: {Class: target.Traversal, Count: 0},
	})
	rep := slo.Check(res)
	if len(rep.Verdicts) != 0 {
		t.Errorf("zero-count class should be skipped, got %d verdicts", len(rep.Verdicts))
	}
}

// TestCheckEmptyReportPassesVacuously proves an empty report passes but carries
// no verdicts.
func TestCheckEmptyReportPassesVacuously(t *testing.T) {
	rep := slo.Check(measure.Result{})
	if !rep.Pass() {
		t.Error("empty report should pass vacuously")
	}
	if len(rep.Verdicts) != 0 {
		t.Errorf("empty result should produce no verdicts, got %d", len(rep.Verdicts))
	}
}

// TestFlatnessPassesUnderRatio proves two runs with ratio below the tolerance
// produce a passing flatness verdict.
func TestFlatnessPassesUnderRatio(t *testing.T) {
	small := makeResult(map[target.Class]measure.Stat{
		target.Traversal: makeStat(target.Traversal, 2*time.Millisecond, 100, 0),
	})
	giant := makeResult(map[target.Class]measure.Stat{
		target.Traversal: makeStat(target.Traversal, 4*time.Millisecond, 100, 0), // 2x ratio
	})
	rep := slo.Flatness(small, giant, 3.0)
	if !rep.Pass() {
		t.Errorf("2x ratio under 3.0x tolerance should pass: %v", rep.Failures())
	}
}

// TestFlatnessFailsOverRatio proves a ratio above the tolerance fails.
func TestFlatnessFailsOverRatio(t *testing.T) {
	small := makeResult(map[target.Class]measure.Stat{
		target.Traversal: makeStat(target.Traversal, 1*time.Millisecond, 100, 0),
	})
	giant := makeResult(map[target.Class]measure.Stat{
		target.Traversal: makeStat(target.Traversal, 4*time.Millisecond, 100, 0), // 4x ratio
	})
	rep := slo.Flatness(small, giant, 3.0)
	if rep.Pass() {
		t.Error("4x ratio over 3.0x tolerance should fail")
	}
}

// TestFlatnessFailsAbsoluteCeiling proves that a giant run over the ceiling
// fails even when the ratio is fine.
func TestFlatnessFailsAbsoluteCeiling(t *testing.T) {
	small := makeResult(map[target.Class]measure.Stat{
		target.PointRead: makeStat(target.PointRead, 400*time.Microsecond, 100, 0),
	})
	giant := makeResult(map[target.Class]measure.Stat{
		target.PointRead: makeStat(target.PointRead, 600*time.Microsecond, 100, 0), // over 500µs ceiling; 1.5x ratio
	})
	rep := slo.Flatness(small, giant, 3.0)
	if rep.Pass() {
		t.Error("giant p99 over ceiling should fail flatness even if ratio is under tolerance")
	}
}

// TestFlatnessSkipsAnalytical proves the analytical class is skipped in
// flatness, same as in Check.
func TestFlatnessSkipsAnalytical(t *testing.T) {
	small := makeResult(map[target.Class]measure.Stat{
		target.Analytical: makeStat(target.Analytical, 10*time.Second, 5, 0),
	})
	giant := makeResult(map[target.Class]measure.Stat{
		target.Analytical: makeStat(target.Analytical, 50*time.Second, 5, 0),
	})
	rep := slo.Flatness(small, giant, 3.0)
	if len(rep.Verdicts) != 0 {
		t.Errorf("analytical class should be skipped in flatness, got %d verdicts", len(rep.Verdicts))
	}
}

// TestFlatnessDefaultTolerance proves a zero tolerance defaults to 3.0.
func TestFlatnessDefaultTolerance(t *testing.T) {
	small := makeResult(map[target.Class]measure.Stat{
		target.Write: makeStat(target.Write, 1*time.Millisecond, 50, 0),
	})
	giant := makeResult(map[target.Class]measure.Stat{
		target.Write: makeStat(target.Write, 2*time.Millisecond, 50, 0), // 2x, well under default 3.0
	})
	rep := slo.Flatness(small, giant, 0) // zero means use default
	if !rep.Pass() {
		t.Errorf("2x ratio with default tolerance should pass: %v", rep.Failures())
	}
}

// TestRegressionPassesFaster proves a candidate faster than the baseline always
// passes.
func TestRegressionPassesFaster(t *testing.T) {
	base := makeResult(map[target.Class]measure.Stat{
		target.Traversal: makeStat(target.Traversal, 4*time.Millisecond, 100, 0),
	})
	cand := makeResult(map[target.Class]measure.Stat{
		target.Traversal: makeStat(target.Traversal, 2*time.Millisecond, 100, 0), // 0.5x ratio
	})
	vv := slo.Regression(base, cand, 1.20)
	for _, v := range vv {
		if !v.Pass {
			t.Errorf("faster candidate should pass regression: %v", v.Reason)
		}
	}
}

// TestRegressionFailsSlower proves a candidate slower than the tolerance fails.
func TestRegressionFailsSlower(t *testing.T) {
	base := makeResult(map[target.Class]measure.Stat{
		target.Traversal: makeStat(target.Traversal, 2*time.Millisecond, 100, 0),
	})
	cand := makeResult(map[target.Class]measure.Stat{
		target.Traversal: makeStat(target.Traversal, 3*time.Millisecond, 100, 0), // 1.5x > 1.20 tolerance
	})
	vv := slo.Regression(base, cand, 1.20)
	found := false
	for _, v := range vv {
		if !v.Pass {
			found = true
		}
	}
	if !found {
		t.Error("1.5x regression over 1.20 tolerance should fail")
	}
}

// TestRegressionSkipsAnalytical proves analytical class is skipped.
func TestRegressionSkipsAnalytical(t *testing.T) {
	base := makeResult(map[target.Class]measure.Stat{
		target.Analytical: makeStat(target.Analytical, 10*time.Second, 5, 0),
	})
	cand := makeResult(map[target.Class]measure.Stat{
		target.Analytical: makeStat(target.Analytical, 50*time.Second, 5, 0),
	})
	vv := slo.Regression(base, cand, 1.20)
	if len(vv) != 0 {
		t.Errorf("analytical should be skipped in regression, got %d verdicts", len(vv))
	}
}

// TestRegressionDefaultTolerance proves a zero tolerance defaults to 1.20.
func TestRegressionDefaultTolerance(t *testing.T) {
	base := makeResult(map[target.Class]measure.Stat{
		target.PointRead: makeStat(target.PointRead, 400*time.Microsecond, 100, 0),
	})
	cand := makeResult(map[target.Class]measure.Stat{
		target.PointRead: makeStat(target.PointRead, 450*time.Microsecond, 100, 0), // 1.125x, under default 1.20
	})
	vv := slo.Regression(base, cand, 0) // zero means use default
	for _, v := range vv {
		if !v.Pass {
			t.Errorf("1.125x with default 1.20 tolerance should pass: %v", v.Reason)
		}
	}
}
