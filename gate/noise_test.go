package gate

import (
	"testing"
	"time"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/measure"
)

// run builds one result from a query id to p50 map.
func run(p50s map[string]time.Duration) measure.Result {
	byQuery := map[string]measure.Stat{}
	for id, d := range p50s {
		byQuery[id] = stat(engine.PointRead, d, d*4)
	}
	return measure.Result{ByQuery: byQuery}
}

func TestMeasureNoiseFindsTheSpread(t *testing.T) {
	us := time.Microsecond
	runs := []measure.Result{
		run(map[string]time.Duration{"steady": 100 * us, "jumpy": 20 * us}),
		run(map[string]time.Duration{"steady": 104 * us, "jumpy": 44 * us}),
		run(map[string]time.Duration{"steady": 102 * us, "jumpy": 31 * us}),
	}
	n := MeasureNoise(runs, 0)
	if n.Runs != 3 {
		t.Fatalf("Runs = %d, want 3", n.Runs)
	}
	if len(n.Spreads) != 4 {
		t.Fatalf("Spreads = %v, want one per query per metric", n.Spreads)
	}
	// Widest first, so the query that needs looking at is on top.
	if n.Spreads[0].Query != "jumpy" {
		t.Errorf("worst spread is %q, want jumpy", n.Spreads[0].Query)
	}
	if got := n.Spreads[0].Factor; got < 2.19 || got > 2.21 {
		t.Errorf("jumpy factor = %.2f, want 44/20 = 2.20", got)
	}
	if got := n.Spreads[0].Median; got != 31*us {
		t.Errorf("jumpy median = %v, want 31µs", got)
	}
	if n.Worst < 2.19 || n.Worst > 2.21 {
		t.Errorf("Worst = %.2f, want 2.20", n.Worst)
	}
}

// A tail is looser than a median. A floor measured from p50 alone leaves the
// gate calling p99 noise a regression, which is the bug this covers.
func TestMeasureNoiseCoversTheTailAndNotJustTheMedian(t *testing.T) {
	us := time.Microsecond
	steady := measure.Result{ByQuery: map[string]measure.Stat{
		"q": {Class: engine.PointRead, Count: 100, P50: 100 * us, P99: 100 * us},
	}}
	tail := measure.Result{ByQuery: map[string]measure.Stat{
		"q": {Class: engine.PointRead, Count: 100, P50: 102 * us, P99: 250 * us},
	}}
	n := MeasureNoise([]measure.Result{steady, tail}, 0)

	var p50, p99 *Spread
	for i := range n.Spreads {
		switch n.Spreads[i].Metric {
		case "p50":
			p50 = &n.Spreads[i]
		case "p99":
			p99 = &n.Spreads[i]
		}
	}
	if p50 == nil || p99 == nil {
		t.Fatalf("Spreads = %v, want both metrics measured", n.Spreads)
	}
	if p99.Factor < 2.4 {
		t.Errorf("p99 factor = %.2f, want the 2.5x tail spread", p99.Factor)
	}
	if p50.Factor > 1.1 {
		t.Errorf("p50 factor = %.2f, want the steady median", p50.Factor)
	}
	// The floor has to cover the tail, or a gate using it fails on p99
	// noise it was measured to have.
	if n.Floor < p99.Factor {
		t.Errorf("Floor = %.2f, under the measured p99 spread of %.2f", n.Floor, p99.Factor)
	}
}

func TestMeasureNoiseNeedsMoreThanOneRun(t *testing.T) {
	n := MeasureNoise([]measure.Result{run(map[string]time.Duration{"q": time.Microsecond})}, 0)
	if n.Floor != 1 || len(n.Spreads) != 0 {
		t.Errorf("one run gave floor %.2f and %d spreads, want no measurement at all", n.Floor, len(n.Spreads))
	}
	if n.Usable(1.10) {
		t.Error("a single run must not be reported as a usable noise measurement")
	}
}

func TestNoiseSaysWhenAMachineCannotHoldTheGate(t *testing.T) {
	us := time.Microsecond
	// Two runs of one unchanged binary that disagree by more than the
	// gate allows: nothing measured here can be trusted at 1.10x.
	loud := MeasureNoise([]measure.Result{
		run(map[string]time.Duration{"q": 20 * us}),
		run(map[string]time.Duration{"q": 44 * us}),
	}, 0)
	if loud.Usable(1.10) {
		t.Errorf("floor %.2f was called usable against a 1.10x gate", loud.Floor)
	}
	quiet := MeasureNoise([]measure.Result{
		run(map[string]time.Duration{"q": 100 * us}),
		run(map[string]time.Duration{"q": 102 * us}),
	}, 0)
	if !quiet.Usable(1.10) {
		t.Errorf("floor %.2f was called unusable against a 1.10x gate", quiet.Floor)
	}
}

func TestNoisePercentileIgnoresOnePathologicalQuery(t *testing.T) {
	us := time.Microsecond
	// Nine steady queries and one wild one. The floor should follow the
	// nine, not the one, or a single flaky query sets the bar for all.
	a := map[string]time.Duration{"wild": 10 * us}
	b := map[string]time.Duration{"wild": 100 * us}
	for _, id := range []string{"q1", "q2", "q3", "q4", "q5", "q6", "q7", "q8", "q9"} {
		a[id], b[id] = 100*us, 101*us
	}
	n := MeasureNoise([]measure.Result{run(a), run(b)}, 0)
	if n.Worst < 9.9 {
		t.Fatalf("Worst = %.2f, want the wild query's 10x", n.Worst)
	}
	if n.Floor > 1.5 {
		t.Errorf("Floor = %.2f, want the steady majority to set it, with the 10x reported as Worst", n.Floor)
	}
}

func TestRegressionInsideTheNoiseFloorIsNotRuledOn(t *testing.T) {
	us := time.Microsecond
	now := run(map[string]time.Duration{"noisy": 130 * us, "real": 400 * us})
	base := run(map[string]time.Duration{"noisy": 100 * us, "real": 100 * us})

	// Without a floor, both are regressions.
	bare := CheckRegression(now, base, Options{})
	for _, v := range bare {
		if v.Kind != "regression" {
			t.Errorf("kind = %q with no floor set, want every finding to be a regression", v.Kind)
		}
	}

	// With a floor of 1.5x, the 1.3x difference is something this machine
	// produces on its own and the 4x difference is not.
	d := Decision{Engine: "zu", Violations: CheckRegression(now, base, Options{NoiseFloor: 1.5})}
	for _, v := range d.Undecided() {
		if v.Where != "noisy" {
			t.Errorf("undecided on %q, want only noisy", v.Where)
		}
	}
	if len(d.Undecided()) == 0 {
		t.Error("the 1.3x difference inside a 1.5x floor was ruled on anyway")
	}
	for _, v := range d.Failures() {
		if v.Where != "real" {
			t.Errorf("failed on %q, want only real", v.Where)
		}
	}
	if len(d.Failures()) == 0 {
		t.Fatal("the 4x difference was excused by a 1.5x noise floor")
	}
	if d.Pass() {
		t.Error("Pass() is true with a real regression outstanding")
	}
	if d.ExitCode() != ExitBudget {
		t.Errorf("ExitCode = %d, want %d", d.ExitCode(), ExitBudget)
	}
}

func TestANoiseFloorNeverExcusesAFailingRunOnItsOwn(t *testing.T) {
	us := time.Microsecond
	// Everything is inside the floor, so nothing is decided and the run
	// passes, but the findings are still carried for the operator to see.
	now := run(map[string]time.Duration{"q": 130 * us})
	base := run(map[string]time.Duration{"q": 100 * us})
	d := Decision{Engine: "zu", Violations: CheckRegression(now, base, Options{NoiseFloor: 1.5})}
	if !d.Pass() {
		t.Error("a run whose only findings are inside the noise floor must pass")
	}
	if d.ExitCode() != ExitPass {
		t.Errorf("ExitCode = %d, want %d", d.ExitCode(), ExitPass)
	}
	if len(d.Undecided()) == 0 {
		t.Error("the findings were dropped instead of reported as undecided")
	}
	// A verification failure alongside them still dominates.
	d.Violations = append(d.Violations, Violation{Kind: "verification", Where: "q", Detail: "wrong answer"})
	if d.Pass() || d.ExitCode() != ExitVerification {
		t.Errorf("Pass = %v, ExitCode = %d, want a verification failure to dominate the floor",
			d.Pass(), d.ExitCode())
	}
}
