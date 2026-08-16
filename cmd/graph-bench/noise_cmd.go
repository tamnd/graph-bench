package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/tamnd/graph-bench/gate"
	"github.com/tamnd/graph-bench/measure"
)

// newNoiseCmd builds the noise verb. It answers the question a failed gate
// raises and cannot answer on its own: did the code get slower, or did the
// machine. Repeated runs of one unchanged binary should agree; how much they
// do not is the floor under every regression number the gate prints.
func newNoiseCmd() *cobra.Command {
	var (
		dir        string
		eng        string
		wlFilter   string
		scale      string
		percentile float64
		regression float64
	)
	cmd := &cobra.Command{
		Use:   "noise",
		Short: "Measure a machine's run-to-run spread, the floor under any regression claim",
		Long: "noise reads repeated results for one engine (--engine) from the lineage " +
			"(--results) and reports how much they disagreed while nothing was changing. " +
			"Group the runs yourself: every document it reads should be the same binary " +
			"on the same dataset, so filter with --workload and --scale when the lineage " +
			"holds more than one. It prints the per-query spread widest first and a " +
			"suggested --noise-floor for gate. Runs of two or more are required; more " +
			"repeats make a tighter floor.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			docs, err := readResults(dir, wlFilter, scale, eng)
			if err != nil {
				return fmt.Errorf("noise: %w", err)
			}
			if len(docs) < 2 {
				return fmt.Errorf("noise: need at least 2 results for engine %q, found %d: run the same workload again before measuring spread",
					eng, len(docs))
			}

			// A spread only means something across runs that differ in
			// nothing but time, so refuse to average over datasets.
			digests := map[string]int{}
			for _, d := range docs {
				digests[d.Condition.DatasetChecksum]++
			}
			if len(digests) > 1 {
				return fmt.Errorf("noise: results span %d datasets %v: narrow with --workload and --scale, because a spread measured across different data is not a spread",
					len(digests), keysOf(digests))
			}

			runs := make([]measure.Result, 0, len(docs))
			for _, d := range docs {
				runs = append(runs, docToResult(d))
			}
			n := gate.MeasureNoise(runs, percentile)

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "noise: %s, %d runs, %s/%s\n\n", eng, n.Runs, docs[0].Workload, docs[0].Condition.Scale)
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "query\tmetric\truns\tmin\tmedian\tmax\tspread")
			for _, s := range n.Spreads {
				fmt.Fprintf(tw, "%s\t%s\t%d\t%v\t%v\t%v\t%.2fx\n",
					s.Query, s.Metric, s.Runs, s.Min, s.Median, s.Max, s.Factor)
			}
			tw.Flush()
			fmt.Fprintf(out, "\nfloor (p%.0f) %.2fx, worst %.2fx\n", n.Percentile, n.Floor, n.Worst)
			fmt.Fprintf(out, "%s\n", n.Summary(regression))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&dir, "results", "results", "lineage directory holding the repeated results")
	f.StringVar(&eng, "engine", "zu", "the engine whose repeats are measured")
	f.StringVar(&wlFilter, "workload", "", "measure only this workload")
	f.StringVar(&scale, "scale", "", "measure only this dataset scale tier")
	f.Float64Var(&percentile, "percentile", gate.DefaultNoisePercentile,
		"percentile of the per-query spreads the suggested floor is taken from")
	f.Float64Var(&regression, "regression-factor", gate.DefaultRegressionFactor,
		"the gate factor the floor is judged against")
	return cmd
}

func keysOf(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
