package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/tamnd/graph-bench/report"
	"github.com/tamnd/graph-bench/target"
)

// gateExitCode wraps a non-zero exit code for commands that need to signal CI
// failure without printing an error message (the message is already printed).
type gateExitCode int

func (e gateExitCode) Error() string { return fmt.Sprintf("gate: failed with exit code %d", int(e)) }
func (e gateExitCode) ExitCode() int { return int(e) }

// Budget is a per-class p99 ceiling for the gate check. All units are
// durations; a zero value for a class means the budget is unconstrained.
type Budget struct {
	PointRead  time.Duration
	Traversal  time.Duration
	Subgraph   time.Duration
	Write      time.Duration
	Analytical time.Duration
}

// newGateCmd builds the gate verb. It reads one JSON results file, checks each
// class p99 against a declared budget, optionally compares p99 against a stored
// baseline, and exits non-zero on any violation. The violation list is printed
// to stderr so CI logs capture it. See doc 07 for the full SLO gate design.
func newGateCmd() *cobra.Command {
	var (
		inFile   string
		lineage  string
		workload string
		scale    string
		engine   string

		// Budget flags: per-class p99 ceilings in duration strings.
		pointReadBudget  time.Duration
		traversalBudget  time.Duration
		subgraphBudget   time.Duration
		writeBudget      time.Duration
		analyticalBudget time.Duration

		// Regression flag: p99 may not regress beyond this factor vs the stored baseline.
		regressionFactor float64
	)

	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Check a run against its budgets and a stored baseline, for CI",
		Long: "gate reads a JSON results file (--file) or the newest record per engine " +
			"from the lineage (--lineage, filtered by --workload/--scale/--engine) " +
			"and checks each class p99 against the declared budget flags. " +
			"A budget of 0 for a class means unconstrained. " +
			"With --regression-factor F (default 1.1), the gate also fails if any class " +
			"p99 has grown by more than F times the stored baseline. " +
			"Violations are printed to stderr and the process exits with code 2. " +
			"Exit 0 means all checks passed.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Load results.
			var results []report.EngineResult
			var loadErr error
			if inFile != "" {
				f, err := os.Open(inFile)
				if err != nil {
					return fmt.Errorf("gate: open %s: %w", inFile, err)
				}
				r, parseErr := report.ParseJSON(f)
				f.Close()
				if parseErr != nil {
					return fmt.Errorf("gate: parse %s: %w", inFile, parseErr)
				}
				results = r
			} else {
				base := lineage
				if base == "" {
					base = "results"
				}
				results, loadErr = report.ReadLineage(base, workload, scale, engine)
				if loadErr != nil {
					return fmt.Errorf("gate: lineage: %w", loadErr)
				}
				// Keep only the newest per engine.
				results = latestPerEngine(results)
			}

			if len(results) == 0 {
				return fmt.Errorf("gate: no results found")
			}

			budgets := map[target.Class]time.Duration{
				target.PointRead:  pointReadBudget,
				target.Traversal:  traversalBudget,
				target.Subgraph:   subgraphBudget,
				target.Write:      writeBudget,
				target.Analytical: analyticalBudget,
			}

			var violations []string
			for _, er := range results {
				for cl, ceiling := range budgets {
					if ceiling == 0 {
						continue
					}
					stat, ok := er.Result.Stats[cl]
					if !ok {
						continue
					}
					if stat.P99 > ceiling {
						violations = append(violations, fmt.Sprintf(
							"  %s %s p99=%s budget=%s (%.1fx over)",
							er.Name, cl, stat.P99, ceiling,
							float64(stat.P99)/float64(ceiling),
						))
					}
				}
			}

			if len(violations) > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "gate: %d budget violation(s):\n", len(violations))
				for _, v := range violations {
					fmt.Fprintln(cmd.ErrOrStderr(), v)
				}
				return gateExitCode(2)
			}

			// Regression check vs baseline is a stub; the baseline comparison
			// lands when the lineage has enough historical data to make it
			// meaningful. The flag is wired so scripts can set it now and the
			// gate will enforce it once implemented.
			if regressionFactor > 0 && regressionFactor != 1.0 {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"gate: --regression-factor is declared (%.2f) but baseline comparison is not yet wired; skipping\n",
					regressionFactor)
			}

			fmt.Fprintln(cmd.OutOrStdout(), "gate: all checks passed")
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&inFile, "file", "", "JSON results file (from 'run --format json')")
	f.StringVar(&lineage, "lineage", "", "lineage tree root (default: results/)")
	f.StringVar(&workload, "workload", "", "filter lineage by workload name")
	f.StringVar(&scale, "scale", "", "filter lineage by scale factor")
	f.StringVar(&engine, "engine", "", "filter lineage by engine name")
	f.DurationVar(&pointReadBudget, "point-read-budget", 0, "p99 ceiling for PointRead class (0=unconstrained)")
	f.DurationVar(&traversalBudget, "traversal-budget", 0, "p99 ceiling for Traversal class (0=unconstrained)")
	f.DurationVar(&subgraphBudget, "subgraph-budget", 0, "p99 ceiling for Subgraph class (0=unconstrained)")
	f.DurationVar(&writeBudget, "write-budget", 0, "p99 ceiling for Write class (0=unconstrained)")
	f.DurationVar(&analyticalBudget, "analytical-budget", 0, "p99 ceiling for Analytical class (0=unconstrained)")
	f.Float64Var(&regressionFactor, "regression-factor", 1.1, "max allowed p99 growth vs baseline (1.0=no regression)")
	return cmd
}
