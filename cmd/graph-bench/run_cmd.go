package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tamnd/graph-bench/measure"
	"github.com/tamnd/graph-bench/report"
	"github.com/tamnd/graph-bench/workload"

	// Import workload families so their init() functions register them.
	_ "github.com/tamnd/graph-bench/workload/lsqb"
	_ "github.com/tamnd/graph-bench/workload/micro"
	_ "github.com/tamnd/graph-bench/workload/snb"
)

// newRunCmd builds the run verb. It resolves the workload and engine list,
// runs the chosen workload against each engine, and writes the results in the
// chosen format. The --publish flag additionally appends each result to the
// append-only lineage under results/. See doc 08 section 1.1 for the full
// contract.
func newRunCmd() *cobra.Command {
	var (
		wlName      string
		engines     []string
		scale        string
		cache        string
		format       string
		outFile      string
		publish      bool
		rate         float64
		concurrency  []int
		lineageDir   string
		warmup       time.Duration
		window       time.Duration
		count        int
		datasetPath  string
		datasetsDir  string
		curateSeed   int64
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a workload against one or more engines and record the measurement",
		Long: "run executes a named workload (--workload) against one or more engines " +
			"(--engines, comma-separated or repeated flags) and emits the result in the " +
			"chosen format (--format). " +
			"The gr in-process adapter is always available; Bolt adapters require -tags bolt. " +
			"With --publish the result is also appended to the lineage tree (--lineage-dir). " +
			"Dataset resolution: use --dataset-path for an explicit path, --datasets-dir to " +
			"search a directory, or let the command auto-generate a synthetic dataset.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Validate and resolve the workload.
			if wlName == "" {
				return fmt.Errorf("run: --workload is required")
			}
			wl, ok := workload.Lookup(wlName)
			if !ok {
				return fmt.Errorf("run: unknown workload %q; run 'graph-bench list workloads' to see registered workloads", wlName)
			}

			// Flatten engine list.
			flat := flattenEngines(engines)
			if len(flat) == 0 {
				flat = []string{"gr"}
			}

			// Validate cache flag.
			switch cache {
			case "warm", "cold", "both", "":
				// ok
			default:
				return fmt.Errorf("run: --cache must be warm|cold|both, got %q", cache)
			}
			if cache == "" {
				cache = "warm"
			}

			// Validate format.
			var outFmt report.Format
			switch format {
			case "table", "":
				outFmt = report.FormatTable
			case "json":
				outFmt = report.FormatJSON
			case "markdown", "md":
				outFmt = report.FormatMarkdown
			case "csv":
				outFmt = report.FormatCSV
			default:
				return fmt.Errorf("run: --format must be table|json|markdown|csv, got %q", format)
			}

			// Build the measurement options.
			opts := measure.Options{
				Rate:   rate,
				Warmup: warmup,
				Count:  count,
			}
			if window > 0 {
				opts.Duration = window
			}
			if len(concurrency) > 0 {
				opts.Concurrency = concurrency[0]
			}

			// Run against each engine.
			var results []report.EngineResult
			ctx := cmd.Context()
			for _, eng := range flat {
				er, runErr := executeRun(ctx, eng, wl, datasetPath, datasetsDir, scale, cache, opts, lineageDir, publish, curateSeed)
				if runErr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "run: engine %s: %v\n", eng, runErr)
					continue
				}
				results = append(results, er)
			}

			if len(results) == 0 {
				return fmt.Errorf("run: all engines failed or produced no result")
			}

			// Open the output writer.
			out := cmd.OutOrStdout()
			if outFile != "" {
				f, err := os.Create(outFile)
				if err != nil {
					return fmt.Errorf("run: create %s: %w", outFile, err)
				}
				defer f.Close()
				out = f
			}

			// Render the results.
			if outFmt == report.FormatJSON {
				return report.RenderJSON(results, out)
			}
			conditions := conditionSummary(results)
			m := report.Assemble(results, report.ColumnClass, conditions)
			return report.Render(m, outFmt, out)
		},
	}

	f := cmd.Flags()
	f.StringVar(&wlName, "workload", "", "workload name (required); see 'list workloads'")
	f.StringArrayVar(&engines, "engines", nil, "engines to run (comma-separated or repeated); default is gr")
	f.StringVar(&scale, "scale", "SF1", "scale factor label for the condition stamp")
	f.StringVar(&cache, "cache", "warm", "cache condition: warm|cold|both")
	f.StringVar(&format, "format", "table", "output format: table|json|markdown|csv")
	f.StringVar(&outFile, "out", "", "output file (default: stdout)")
	f.BoolVar(&publish, "publish", false, "append results to the lineage tree")
	f.StringVar(&lineageDir, "lineage-dir", "results", "lineage tree root")
	f.Float64Var(&rate, "rate", 0, "offered queries/second (0 = sequential)")
	f.DurationVar(&warmup, "warmup", 0, "warmup duration before measurement begins (only effective with --rate; count-based runs skip warmup)")
	f.DurationVar(&window, "window", 30*time.Second, "steady-state measurement window")
	f.IntVar(&count, "count", 0, "fixed query count per query (overrides --window if > 0)")
	f.IntSliceVar(&concurrency, "concurrency", nil, "concurrency sweep points")
	f.StringVar(&datasetPath, "dataset-path", "", "path to an existing materialized dataset directory")
	f.StringVar(&datasetsDir, "datasets-dir", "datasets", "directory of pre-generated datasets; searched by manifest name")
	f.Int64Var(&curateSeed, "curate-seed", 1, "PRNG seed for the parameter curation step")

	_ = cmd.MarkFlagRequired("workload")
	return cmd
}

// flattenEngines splits comma-separated engine names from the flag slice.
func flattenEngines(names []string) []string {
	var flat []string
	for _, n := range names {
		for _, part := range strings.Split(n, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				flat = append(flat, part)
			}
		}
	}
	return flat
}
