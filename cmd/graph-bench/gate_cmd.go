package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/gate"
	"github.com/tamnd/graph-bench/measure"
	"github.com/tamnd/graph-bench/report"
)

// gateErr carries the gate's exit code (0 pass, 2 budget/regression,
// 3 verification — spec 08 §9) out through cobra without re-printing.
type gateErr int

func (e gateErr) Error() string { return fmt.Sprintf("gate: failed with exit code %d", int(e)) }
func (e gateErr) ExitCode() int { return int(e) }

// newGateCmd builds the gate verb. It gates exactly one engine (default zu)
// on absolute budgets, regression vs a stored baseline, and verification
// integrity; the comparative matrix reports competitors, it never fails them
// (F-contract).
func newGateCmd() *cobra.Command {
	var (
		inFile     string
		dir        string
		baseline   string
		gateEngine string
		wlFilter   string
		regression float64
		noiseFloor float64
	)
	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Check the gated engine's latest results against budgets and baseline, for CI",
		Long: "gate reads the newest result per workload for the gated engine " +
			"(--gate-engine, default zu) from the lineage (--results) or one file " +
			"(--file) and checks: absolute per-class budgets for the engine's plane, " +
			"per-query p50/p99 regression against the baseline results (--baseline, " +
			"a lineage directory or a single JSON file), and verification integrity " +
			"(any FAIL, any SKIP the baseline had as PASS). Exit codes: 0 pass, " +
			"2 budget/regression violation, 3 verification failure.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Load the candidate documents.
			var docs []*report.Document
			if inFile != "" {
				doc, err := report.Read(inFile)
				if err != nil {
					return fmt.Errorf("gate: %w", err)
				}
				docs = []*report.Document{doc}
			} else {
				all, err := readResults(dir, wlFilter, "", gateEngine)
				if err != nil {
					return fmt.Errorf("gate: %w", err)
				}
				docs = latestPerWorkload(all)
			}
			if len(docs) == 0 {
				return fmt.Errorf("gate: no results for engine %q", gateEngine)
			}

			// Load baseline documents, newest per workload, when given.
			baseDocs := map[string]*report.Document{}
			if baseline != "" {
				var bd []*report.Document
				if doc, err := report.Read(baseline); err == nil {
					bd = []*report.Document{doc}
				} else if all, err := readResults(baseline, wlFilter, "", gateEngine); err == nil {
					bd = latestPerWorkload(all)
				}
				for _, d := range bd {
					baseDocs[d.Workload] = d
				}
			}

			d := gate.Decision{Engine: gateEngine}
			for _, doc := range docs {
				res := docToResult(doc)
				plane := engine.Plane(doc.Condition.Plane)
				d.Violations = append(d.Violations, gate.CheckBudgets(res, plane, doc.Workload)...)
				if base, ok := baseDocs[doc.Workload]; ok {
					d.Violations = append(d.Violations,
						gate.CheckRegression(res, docToResult(base), gate.Options{
							RegressionFactor: regression,
							NoiseFloor:       noiseFloor,
						})...)
				}
				d.Violations = append(d.Violations, checkDocVerification(doc, baseDocs[doc.Workload])...)
			}

			out := cmd.OutOrStdout()
			// Findings inside the noise floor are printed either way: they
			// are the difference between a run that passed and a run that
			// passed because the machine could not tell.
			if undecided := d.Undecided(); len(undecided) > 0 {
				fmt.Fprintf(out, "gate: %s: %d finding(s) inside the %.2fx noise floor, not ruled on:\n",
					gateEngine, len(undecided), noiseFloor)
				for _, v := range undecided {
					fmt.Fprintf(out, "  %s\n", v)
				}
			}
			failures := d.Failures()
			if d.Pass() {
				fmt.Fprintf(out, "gate: %s: all checks passed (%d workload(s))\n", gateEngine, len(docs))
				return nil
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "gate: %s: %d violation(s):\n", gateEngine, len(failures))
			for _, v := range failures {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", v)
			}
			return gateErr(d.ExitCode())
		},
	}
	f := cmd.Flags()
	f.StringVar(&inFile, "file", "", "single JSON result document to gate")
	f.StringVar(&dir, "results", "results", "lineage directory holding the candidate results")
	f.StringVar(&baseline, "baseline", "", "baseline lineage directory or JSON file")
	f.StringVar(&gateEngine, "gate-engine", "zu", "the one engine the gate applies to")
	f.StringVar(&wlFilter, "workload", "", "gate only this workload")
	f.Float64Var(&regression, "regression-factor", gate.DefaultRegressionFactor,
		"allowed p50/p99 growth over the baseline")
	f.Float64Var(&noiseFloor, "noise-floor", 0,
		"this machine's measured run-to-run spread; differences within it are reported, not failed (see 'graph-bench noise')")
	return cmd
}

// latestPerWorkload keeps the newest document per workload name.
func latestPerWorkload(docs []*report.Document) []*report.Document {
	seen := map[string]int{}
	var out []*report.Document
	for _, d := range docs {
		if idx, ok := seen[d.Workload]; !ok {
			seen[d.Workload] = len(out)
			out = append(out, d)
		} else if !d.Condition.StartedAt.Before(out[idx].Condition.StartedAt) {
			out[idx] = d
		}
	}
	return out
}

// docToResult maps a stored document back into a measure.Result so the gate
// checks (which take live results) apply to lineage records too.
func docToResult(doc *report.Document) measure.Result {
	stats := map[engine.Class]measure.Stat{}
	for name, cs := range doc.Classes {
		stats[engine.Class(name)] = classStatToStat(engine.Class(name), cs)
	}
	byQuery := map[string]measure.Stat{}
	for id, cs := range doc.Queries {
		byQuery[id] = classStatToStat("", cs)
	}
	return measure.Result{
		Stats:     stats,
		ByQuery:   byQuery,
		Latency:   doc.Condition.LatencyModel,
		Condition: doc.Condition,
	}
}

func classStatToStat(cl engine.Class, cs report.ClassStat) measure.Stat {
	return measure.Stat{
		Class:         cl,
		Count:         cs.Count,
		Errors:        cs.Errors,
		Min:           cs.Min,
		P50:           cs.P50,
		P90:           cs.P90,
		P95:           cs.P95,
		P99:           cs.P99,
		Max:           cs.Max,
		Mean:          cs.Mean,
		StdDev:        cs.StdDev,
		Throughput:    cs.Throughput,
		RowThroughput: cs.RowThroughput,
	}
}

// checkDocVerification applies the verification-integrity rules (spec 08 §9
// item 4) to a stored document: any FAIL fails the gate, and any SKIP that
// the baseline document verified as PASS is a coverage regression.
func checkDocVerification(doc, base *report.Document) []gate.Violation {
	baselinePass := map[string]bool{}
	if base != nil {
		for _, v := range base.Verification {
			if v.Outcome == "PASS" {
				baselinePass[v.QueryID] = true
			}
		}
	}
	var out []gate.Violation
	for _, v := range doc.Verification {
		switch v.Outcome {
		case "FAIL":
			out = append(out, gate.Violation{Kind: "verification", Where: v.QueryID, Detail: v.Reason})
		case "SKIP":
			if baselinePass[v.QueryID] {
				out = append(out, gate.Violation{
					Kind:  "verification",
					Where: v.QueryID,
					Detail: fmt.Sprintf("coverage regression: baseline PASS is now SKIP (%s)",
						v.Reason),
				})
			}
		}
	}
	return out
}
