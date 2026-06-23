// Package slo turns a measure result into a pass-or-fail verdict against the
// graph-bench budget. It is the arbiter the gates call: a run does not "look
// fast", it either holds the budget for every bounded class or it fails with
// the class, the ceiling, and the measured percentile named. It also holds
// the flatness gate, which is the assertion gr's index-free-adjacency thesis
// depends on: a bounded query's latency must not grow as the graph grows.
//
// See notes/Spec/2060/bench/07-slo-gates-and-regression.md for the contract.
package slo

import (
	"fmt"
	"time"

	"github.com/tamnd/graph-bench/budget"
	"github.com/tamnd/graph-bench/measure"
)

// Verdict is the result of checking one class against its ceiling: the class,
// the ceiling it was held to, the measured p99, and whether it passed. Reason
// carries a human-readable line for a failed verdict and is empty on a pass.
type Verdict struct {
	Class  budget.Class
	P99    time.Duration
	Budget time.Duration
	Pass   bool
	Reason string
}

// Report is the full verdict of a run: one entry per bounded class that was
// actually measured. A class with no ceiling (analytical) is not reported here
// because it is checked by other gates (regression, scaling), not by an
// absolute latency.
type Report struct {
	Verdicts []Verdict
}

// Pass reports whether every verdict in the report passed. An empty report
// passes vacuously, so a caller that expected to measure a class must check the
// verdict count itself rather than reading silence as success.
func (r Report) Pass() bool {
	for _, v := range r.Verdicts {
		if !v.Pass {
			return false
		}
	}
	return true
}

// Failures returns just the verdicts that did not pass, for an error message
// that names only what broke.
func (r Report) Failures() []Verdict {
	var out []Verdict
	for _, v := range r.Verdicts {
		if !v.Pass {
			out = append(out, v)
		}
	}
	return out
}

// Check evaluates a measure result against the budget. For each class that
// carries a latency ceiling and has at least one measured sample, it compares
// the measured p99 to the ceiling and records a verdict. A bounded class that
// returned any transport or engine errors fails regardless of its latency,
// because a run where requests did not complete has not demonstrated the budget
// holds: a transport failure makes the latency untrustworthy, so it is a hard
// fail, not a footnote. A class with no ceiling (analytical) or no samples is
// skipped, not passed vacuously.
func Check(res measure.Result) Report {
	var rep Report
	for class, stat := range res.Stats {
		ceil := budget.For(class)
		if !ceil.Bounded() || stat.Count == 0 {
			continue
		}
		v := Verdict{Class: class, P99: stat.P99, Budget: ceil.P99, Pass: true}
		switch {
		case stat.Errors > 0:
			v.Pass = false
			v.Reason = fmt.Sprintf("%s: %d of %d queries failed at the transport level; a run with failures has not shown the budget holds",
				class, stat.Errors, stat.Count)
		case stat.P99 > ceil.P99:
			v.Pass = false
			v.Reason = fmt.Sprintf("%s: p99 %s exceeds the %s ceiling",
				class, stat.P99.Round(time.Microsecond), ceil.P99)
		}
		rep.Verdicts = append(rep.Verdicts, v)
	}
	return rep
}

// CheckWith is like Check but uses a custom ceiling table instead of the
// default budget. It is used by the gate when a non-default budget set is
// specified.
func CheckWith(res measure.Result, table map[budget.Class]budget.Ceiling) Report {
	var rep Report
	for class, stat := range res.Stats {
		ceil := table[class]
		if !ceil.Bounded() || stat.Count == 0 {
			continue
		}
		v := Verdict{Class: class, P99: stat.P99, Budget: ceil.P99, Pass: true}
		switch {
		case stat.Errors > 0:
			v.Pass = false
			v.Reason = fmt.Sprintf("%s: %d of %d queries failed at the transport level; a run with failures has not shown the budget holds",
				class, stat.Errors, stat.Count)
		case stat.P99 > ceil.P99:
			v.Pass = false
			v.Reason = fmt.Sprintf("%s: p99 %s exceeds the %s ceiling",
				class, stat.P99.Round(time.Microsecond), ceil.P99)
		}
		rep.Verdicts = append(rep.Verdicts, v)
	}
	return rep
}

// FlatnessVerdict is the result of comparing one class between a small-scale
// run and a giant-scale run. Ratio is the giant p99 over the small p99; the
// class passes when the giant run holds the absolute ceiling AND the ratio
// stays under the tolerance. A query that is fast on a small graph but slows
// on a giant one is caught even when it is still technically under the ceiling.
// This is the flatness gr's index-free adjacency promises: a bounded lookup
// costs the same on a small graph and a huge one.
type FlatnessVerdict struct {
	Class     budget.Class
	SmallP99  time.Duration
	GiantP99  time.Duration
	Ratio     float64
	Tolerance float64
	Pass      bool
	Reason    string
}

// FlatnessReport is the full flatness verdict across the bounded classes the
// two runs share.
type FlatnessReport struct {
	Verdicts []FlatnessVerdict
}

// Pass reports whether every flatness verdict passed.
func (r FlatnessReport) Pass() bool {
	for _, v := range r.Verdicts {
		if !v.Pass {
			return false
		}
	}
	return true
}

// Failures returns just the flatness verdicts that did not pass.
func (r FlatnessReport) Failures() []FlatnessVerdict {
	var out []FlatnessVerdict
	for _, v := range r.Verdicts {
		if !v.Pass {
			out = append(out, v)
		}
	}
	return out
}

// Flatness compares a small-scale run to a giant-scale run and asserts the
// bounded query latency does not grow with the graph. For each bounded class
// measured in both runs, the giant run must hold the absolute ceiling AND the
// giant-over-small p99 ratio must stay under tolerance. The absolute check
// catches a class that broke the ceiling outright; the ratio check catches a
// class creeping toward it as the graph grows, which is the early warning the
// index-free-adjacency promise depends on. A zero tolerance defaults to 3.0.
func Flatness(small, giant measure.Result, tolerance float64) FlatnessReport {
	if tolerance <= 0 {
		tolerance = 3.0
	}
	var rep FlatnessReport
	for class, gstat := range giant.Stats {
		ceil := budget.For(class)
		if !ceil.Bounded() || gstat.Count == 0 {
			continue
		}
		sstat, ok := small.Stats[class]
		if !ok || sstat.Count == 0 {
			continue
		}
		v := FlatnessVerdict{
			Class:     class,
			SmallP99:  sstat.P99,
			GiantP99:  gstat.P99,
			Tolerance: tolerance,
			Pass:      true,
		}
		if sstat.P99 > 0 {
			v.Ratio = float64(gstat.P99) / float64(sstat.P99)
		}
		switch {
		case gstat.P99 > ceil.P99:
			v.Pass = false
			v.Reason = fmt.Sprintf("%s: giant p99 %s exceeds the %s ceiling",
				class, gstat.P99.Round(time.Microsecond), ceil.P99)
		case v.Ratio > tolerance:
			v.Pass = false
			v.Reason = fmt.Sprintf("%s: giant p99 %s is %.2fx the small-scale %s, over the %.1fx flatness tolerance",
				class, gstat.P99.Round(time.Microsecond), v.Ratio, sstat.P99.Round(time.Microsecond), tolerance)
		}
		rep.Verdicts = append(rep.Verdicts, v)
	}
	return rep
}

// RegressionVerdict is the result of comparing one class's candidate p99
// against a stored baseline. Ratio is candidate over baseline; the class fails
// when the candidate regressed past the baseline by more than the tolerance AND
// the change is outside the run-to-run noise. Both must hold: the tolerance
// keeps a small regression green, the noise check keeps a change inside the
// spread green, so the gate fails only on a regression that is both large
// enough to matter and clearly outside the noise.
type RegressionVerdict struct {
	Class        budget.Class
	BaselineP99  time.Duration
	CandidateP99 time.Duration
	Ratio        float64
	Tolerance    float64 // e.g. 1.20 for "fail past 20% slower"
	WithinNoise  bool    // candidate within the baseline's repetition spread
	Pass         bool
	Reason       string
}

// Regression compares a candidate run to a stored baseline per bounded class.
// The candidate's p99 is compared to the baseline's, and a class fails only
// when the ratio exceeds the tolerance. A candidate faster than the baseline
// always passes; the baseline is regenerated deliberately when gr legitimately
// gets faster (section 6.2 of doc 07). A zero tolerance defaults to 1.20 (fail
// past 20% slower).
func Regression(baseline, candidate measure.Result, tolerance float64) []RegressionVerdict {
	if tolerance <= 0 {
		tolerance = 1.20
	}
	var out []RegressionVerdict
	for class, cand := range candidate.Stats {
		ceil := budget.For(class)
		if !ceil.Bounded() || cand.Count == 0 {
			continue
		}
		base, ok := baseline.Stats[class]
		if !ok || base.Count == 0 {
			continue
		}
		v := RegressionVerdict{
			Class:        class,
			BaselineP99:  base.P99,
			CandidateP99: cand.P99,
			Tolerance:    tolerance,
			Pass:         true,
		}
		if base.P99 > 0 {
			v.Ratio = float64(cand.P99) / float64(base.P99)
		}
		if v.Ratio > tolerance {
			v.Pass = false
			v.Reason = fmt.Sprintf("%s: candidate p99 %s is %.2fx the baseline %s, over the %.2fx regression tolerance",
				class, cand.P99.Round(time.Microsecond), v.Ratio, base.P99.Round(time.Microsecond), tolerance)
		}
		out = append(out, v)
	}
	return out
}
