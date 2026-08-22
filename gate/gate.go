package gate

import (
	"fmt"
	"strings"
	"time"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/measure"
	"github.com/tamnd/graph-bench/verify"
)

// Violation is one gate finding with enough context to act on. All kinds but
// "indeterminate" are failures.
type Violation struct {
	// Kind is "budget", "regression", "flatness", "verification", or
	// "indeterminate". The last one is a measurement the gate declines to
	// rule on because the machine it ran on is noisier than the difference
	// being claimed; it is reported and does not fail the run.
	Kind string

	// Where names the query or class the violation is about.
	Where string

	// Detail is the human-readable explanation with both numbers.
	Detail string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s[%s]: %s", v.Kind, v.Where, v.Detail)
}

// Decision is the gate's verdict over one engine's run set.
type Decision struct {
	Engine     string
	Violations []Violation
}

// ExitCode maps the decision to the process exit code (spec 08 §9):
// verification failures dominate budget/regression violations, and
// indeterminate findings decide nothing.
func (d Decision) ExitCode() int {
	code := ExitPass
	for _, v := range d.Violations {
		if v.Kind == Indeterminate {
			continue
		}
		if v.Kind == "verification" {
			return ExitVerification
		}
		code = ExitBudget
	}
	return code
}

func (d Decision) Pass() bool { return len(d.Failures()) == 0 }

// Failures are the findings that fail the run.
func (d Decision) Failures() []Violation { return d.byKind(false) }

// Undecided are the findings the gate refused to rule on, because the
// difference is smaller than the run-to-run spread the machine was measured
// to have. They are the answer to "is this a regression or is this the
// laptop", and the honest answer is that this run cannot tell.
func (d Decision) Undecided() []Violation { return d.byKind(true) }

func (d Decision) byKind(indeterminate bool) []Violation {
	var out []Violation
	for _, v := range d.Violations {
		if (v.Kind == Indeterminate) == indeterminate {
			out = append(out, v)
		}
	}
	return out
}

// Options tunes the gate.
type Options struct {
	// RegressionFactor is the allowed p50/p99 growth over baseline;
	// 0 means DefaultRegressionFactor.
	RegressionFactor float64

	// FlatnessFactor bounds the small-vs-large PointRead latency ratio;
	// 0 means DefaultFlatnessFactor.
	FlatnessFactor float64

	// NoiseFloor is the run-to-run spread the machine running the gate was
	// measured to have, as a factor: 1.8 means two runs of one unchanged
	// binary have been seen to differ by 1.8x. A ratio over the regression
	// factor but within this is reported as indeterminate rather than as a
	// regression, because a gate cannot honestly call a difference it could
	// have produced by running the same binary twice. Zero disables the
	// check and every ratio over the factor is a regression, which is the
	// right default for the controlled machine the full matrix runs on.
	//
	// Measure it, do not guess it: `graph-bench noise` runs one engine
	// repeatedly and prints the floor its numbers support.
	NoiseFloor float64
}

func (o Options) regression() float64 {
	if o.RegressionFactor <= 0 {
		return DefaultRegressionFactor
	}
	return o.RegressionFactor
}

func (o Options) flatness() float64 {
	if o.FlatnessFactor <= 0 {
		return DefaultFlatnessFactor
	}
	return o.FlatnessFactor
}

// CheckBudgets evaluates the absolute class budgets (§6) against one
// result. Analytical is tracked, never gated. The fb-* SLO and snb-short
// G2 checks apply when the workload name matches.
func CheckBudgets(res measure.Result, plane engine.Plane, workloadName string) []Violation {
	var out []Violation
	for class, stat := range res.Stats {
		ceiling, ok := BudgetFor(class, plane)
		if !ok {
			continue
		}
		// The write ceiling is the table's or the disk's, whichever is
		// higher: a durable commit costs a sync and no engine goes
		// below one, so on slow storage the table's number is a
		// statement about the drive.
		source := "spec 08 §6"
		if class == engine.Write {
			raised := false
			ceiling, raised = WriteCeiling(plane, res.Condition.Hardware.SyncNanos)
			if raised {
				source = fmt.Sprintf("%d durable syncs of %v, over the spec 08 §6 ceiling",
					DurableWriteSyncs, time.Duration(res.Condition.Hardware.SyncNanos))
			}
		}
		if stat.P99 > ceiling {
			out = append(out, Violation{
				Kind:  "budget",
				Where: string(class),
				Detail: fmt.Sprintf("p99 %v over the %v %s/%s ceiling (%s)",
					stat.P99, ceiling, class, plane, source),
			})
		}
	}
	if strings.HasPrefix(workloadName, "fb") {
		for id, stat := range res.ByQuery {
			if stat.Class == engine.Write {
				continue
			}
			if stat.P99 > FinBenchReadP99 {
				out = append(out, Violation{
					Kind:  "budget",
					Where: id,
					Detail: fmt.Sprintf("p99 %v over the FinBench read SLO %v",
						stat.P99, FinBenchReadP99),
				})
			}
		}
	}
	if workloadName == "snb-short" && plane == engine.InProc {
		for id, stat := range res.ByQuery {
			if stat.Class == engine.Write {
				continue
			}
			if stat.P50 > SNBShortWarmP50 {
				out = append(out, Violation{
					Kind:  "budget",
					Where: id,
					Detail: fmt.Sprintf("warm p50 %v over the %v G2 target",
						stat.P50, SNBShortWarmP50),
				})
			}
		}
	}
	return out
}

// CheckRegression compares per-query p50/p99 against a stored baseline.
// Queries absent from the baseline are new coverage, not regressions.
func CheckRegression(res, baseline measure.Result, opts Options) []Violation {
	factor := opts.regression()
	var out []Violation
	for id, stat := range res.ByQuery {
		base, ok := baseline.ByQuery[id]
		if !ok || base.Count == 0 {
			continue
		}
		check := func(metric string, now, was time.Duration) {
			if was <= 0 {
				return
			}
			ratio := float64(now) / float64(was)
			if ratio <= factor {
				return
			}
			if opts.NoiseFloor > 0 && ratio <= opts.NoiseFloor {
				out = append(out, Violation{
					Kind:  Indeterminate,
					Where: id,
					Detail: fmt.Sprintf("%s %v vs baseline %v (%.2fx > %.2fx allowed, but within the %.2fx noise floor)",
						metric, now, was, ratio, factor, opts.NoiseFloor),
				})
				return
			}
			out = append(out, Violation{
				Kind:  "regression",
				Where: id,
				Detail: fmt.Sprintf("%s %v vs baseline %v (%.2fx > %.2fx allowed)",
					metric, now, was, ratio, factor),
			})
		}
		check("p50", stat.P50, base.P50)
		check("p99", stat.P99, base.P99)
	}
	return out
}

// CheckFlatness compares PointRead p50 between a small and a large
// dataset run: a working index is flat (§9 item 3).
func CheckFlatness(small, large measure.Result, opts Options) []Violation {
	s, okS := small.Stats[engine.PointRead]
	l, okL := large.Stats[engine.PointRead]
	if !okS || !okL || s.P50 <= 0 || l.P50 <= 0 {
		return nil
	}
	factor := opts.flatness()
	if ratio := float64(l.P50) / float64(s.P50); ratio > factor {
		return []Violation{{
			Kind:  "flatness",
			Where: string(engine.PointRead),
			Detail: fmt.Sprintf("p50 grew %.2fx from small (%v) to large (%v), allowed %.2fx — index not working",
				ratio, s.P50, l.P50, factor),
		}}
	}
	return nil
}

// CheckVerification fails on any FAIL, on a poisoned run, and on
// coverage regression: a query the baseline verified as PASS that now
// SKIPs (§9 item 4). baselinePass may be nil when no baseline exists.
func CheckVerification(plan *verify.Plan, baselinePass map[string]bool) []Violation {
	var out []Violation
	if plan.Poisoned {
		out = append(out, Violation{
			Kind: "verification", Where: plan.Workload.Name,
			Detail: "teardown failed: run poisoned, stationarity gone",
		})
	}
	for _, rep := range plan.Reports {
		switch rep.Outcome {
		case verify.Fail:
			out = append(out, Violation{
				Kind: "verification", Where: rep.QueryID,
				Detail: rep.Reason,
			})
		case verify.Skip:
			if baselinePass[rep.QueryID] {
				out = append(out, Violation{
					Kind: "verification", Where: rep.QueryID,
					Detail: fmt.Sprintf("coverage regression: baseline PASS is now SKIP (%s)", rep.Reason),
				})
			}
		}
	}
	return out
}
