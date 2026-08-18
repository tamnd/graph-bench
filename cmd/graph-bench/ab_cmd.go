package main

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/tamnd/graph-bench/gate"
	"github.com/tamnd/graph-bench/measure"
	"github.com/tamnd/graph-bench/report"
)

// newABCmd builds the ab verb. It answers the question the gate cannot answer
// on a machine that is doing something else: is this change slower, when both
// sides of it were measured under the same load.
func newABCmd() *cobra.Command {
	var (
		beforeDir string
		afterDir  string
		eng       string
		wlFilter  string
		scale     string
		metric    string
		threshold float64
		all       bool
	)
	cmd := &cobra.Command{
		Use:   "ab",
		Short: "Compare two builds of one engine run against each other, best of N per side",
		Long: "ab reads repeated results for one engine (--engine) from two lineages, " +
			"--before and --after, and reports what the change did to every query both " +
			"sides ran. Each side is reduced to its best value per query (--metric, " +
			"default min), which is the statistic load cannot inflate, and the rows " +
			"come out slowest first. Run the two sides alternately and swap which one " +
			"goes first every round: load that climbs through a round otherwise lands " +
			"entirely on whichever side ran second. Exit codes: 0 nothing regressed, " +
			"2 a query at or over --factor.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if beforeDir == "" || afterDir == "" {
				return fmt.Errorf("ab: --before and --after are both required")
			}
			if !knownMetric(metric) {
				return fmt.Errorf("ab: unknown metric %q, want one of %s",
					metric, strings.Join(gate.PairedMetrics, ", "))
			}
			before, beforeCost, err := abSide(beforeDir, wlFilter, scale, eng)
			if err != nil {
				return err
			}
			after, afterCost, err := abSide(afterDir, wlFilter, scale, eng)
			if err != nil {
				return err
			}

			p := gate.PairedAB(before, after, metric, threshold)
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "ab: %s, %s before against %s after, %s\n\n", eng, beforeDir, afterDir, p.Metric)
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "workload\tquery\tbefore\tafter\tfactor\truns")
			folded := 0
			for _, pair := range p.Pairs {
				if !all && pair.Factor < 1 && len(p.Pairs) > abHead {
					folded++
					continue
				}
				fmt.Fprintf(tw, "%s\t%s\t%v\t%v\t%.2fx\t%d/%d\n",
					pair.Workload, pair.Query, pair.Before, pair.After, pair.Factor,
					pair.BeforeRuns, pair.AfterRuns)
			}
			tw.Flush()
			if folded > 0 {
				fmt.Fprintf(out, "\n%d row(s) the change made faster are folded away, pass --all for every row\n", folded)
			}

			// What the two sides cost the machine, printed and not ruled
			// on. A change that buys latency with a third more CPU is a
			// trade somebody should get to see, and on a machine that is
			// doing something else these numbers are steadier than the
			// clock: they were what settled the first comparison this
			// command was written for.
			if costs := gate.PairedCosts(beforeCost, afterCost); len(costs) > 0 {
				fmt.Fprintf(out, "\ncost, best of N per side, reported and not gated\n\n")
				ctw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				fmt.Fprintln(ctw, "workload\tmetric\tbefore\tafter\tfactor\truns")
				for _, c := range costs {
					fmt.Fprintf(ctw, "%s\t%s\t%s\t%s\t%.2fx\t%d/%d\n",
						c.Workload, c.Metric, costCell(c.Metric, c.Before), costCell(c.Metric, c.After),
						c.Factor, c.BeforeRuns, c.AfterRuns)
				}
				ctw.Flush()
			}

			fmt.Fprintf(out, "\n%s\n", p.Summary())
			if p.Pass() {
				return nil
			}
			return gateErr(2)
		},
	}
	f := cmd.Flags()
	f.StringVar(&beforeDir, "before", "", "lineage directory holding the repeats from before the change")
	f.StringVar(&afterDir, "after", "", "lineage directory holding the repeats from after it")
	f.StringVar(&eng, "engine", "zu", "the engine whose two builds are compared")
	f.StringVar(&wlFilter, "workload", "", "compare only this workload")
	f.StringVar(&scale, "scale", "", "compare only this scale")
	f.StringVar(&metric, "metric", gate.DefaultPairedMetric,
		"the statistic to compare, one of min, p50, p99")
	f.Float64Var(&threshold, "factor", gate.DefaultRegressionFactor,
		"the growth a query has to reach to count as regressed")
	f.BoolVar(&all, "all", false, "print every row, including the ones the change made faster")
	return cmd
}

// abHead is how many rows a comparison has to hold before the faster ones are
// folded away. Under it the whole table fits on a screen and hiding half of it
// helps nobody.
const abHead = 12

// knownMetric reports whether the paired comparison can compare this one.
func knownMetric(metric string) bool {
	for _, m := range gate.PairedMetrics {
		if m == metric {
			return true
		}
	}
	return false
}

// costCell renders one cost number in its own unit.
func costCell(metric string, v int64) string {
	if metric == "cpu" {
		return report.Nanos(v)
	}
	return report.Bytes(v)
}

// abSide reads one side's repeats, grouped by workload, as latencies and as
// what the run cost. Every document under the directory counts: a paired
// comparison wants all the repeats, not the newest one, because best-of-N is
// what makes it hold up on a busy machine.
func abSide(dir, wl, scale, eng string) (map[string][]measure.Result, map[string][]measure.Resource, error) {
	docs, err := readResults(dir, wl, scale, eng)
	if err != nil {
		return nil, nil, fmt.Errorf("ab: %s: %w", dir, err)
	}
	if len(docs) == 0 {
		return nil, nil, fmt.Errorf("ab: %s: no results for engine %q", dir, eng)
	}
	byWorkload := map[string][]measure.Result{}
	costs := map[string][]measure.Resource{}
	for _, doc := range docs {
		byWorkload[doc.Workload] = append(byWorkload[doc.Workload], docToResult(doc))
		costs[doc.Workload] = append(costs[doc.Workload], doc.Resource)
	}
	return byWorkload, costs, nil
}
