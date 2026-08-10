package gate

import (
	"fmt"
	"strings"
	"time"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/measure"
	"github.com/tamnd/graph-bench/verify"
)

// Violation is one gate failure with enough context to act on.
type Violation struct {
	// Kind is "budget", "regression", "flatness", or "verification".
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
// verification failures dominate budget/regression violations.
func (d Decision) ExitCode() int {
	code := ExitPass
	for _, v := range d.Violations {
		if v.Kind == "verification" {
			return ExitVerification
		}
		code = ExitBudget
	}
	return code
}

func (d Decision) Pass() bool { return len(d.Violations) == 0 }

// Options tunes the gate.
type Options struct {
	// RegressionFactor is the allowed p50/p99 growth over baseline;
	// 0 means DefaultRegressionFactor.
	RegressionFactor float64

	// FlatnessFactor bounds the small-vs-large PointRead latency ratio;
	// 0 means DefaultFlatnessFactor.
	FlatnessFactor float64
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
		if stat.P99 > ceiling {
			out = append(out, Violation{
				Kind:  "budget",
				Where: string(class),
				Detail: fmt.Sprintf("p99 %v over the %v %s/%s ceiling (spec 08 §6)",
					stat.P99, ceiling, class, plane),
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
			if ratio := float64(now) / float64(was); ratio > factor {
				out = append(out, Violation{
					Kind:  "regression",
					Where: id,
					Detail: fmt.Sprintf("%s %v vs baseline %v (%.2fx > %.2fx allowed)",
						metric, now, was, ratio, factor),
				})
			}
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
