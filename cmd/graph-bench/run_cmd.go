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
// contract. Engine adapters are resolved at startup; this slice ships the CLI
// wiring and the measurement loop, not the adapters themselves.
func newRunCmd() *cobra.Command {
	var (
		wlName      string
		engines     []string
		scale       string
		cache       string
		format      string
		outFile     string
		publish     bool
		rate        float64
		concurrency []int
		lineageDir  string
		warmup      time.Duration
		window      time.Duration
		count       int
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a workload against one or more engines and record the measurement",
		Long: "run executes a named workload (--workload) against one or more engines " +
			"(--engines, comma-separated or repeated flags) and emits the result in the " +
			"chosen format (--format). " +
			"With --publish the result is also appended to the lineage tree (--lineage-dir) " +
			"as an append-only JSON record. " +
			"Engine adapters must be registered; the in-process gr adapter is always available; " +
			"Bolt adapters require -tags bolt at build time.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Validate and resolve the workload.
			if wlName == "" {
				return fmt.Errorf("run: --workload is required")
			}
			wl, ok := workload.Lookup(wlName)
			if !ok {
				return fmt.Errorf("run: unknown workload %q; run 'graph-bench list workloads' to see registered workloads", wlName)
			}

			// Resolve engine list.
			resolved, err := resolveEngines(engines)
			if err != nil {
				return err
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
				Rate:        rate,
				Warmup:      warmup,
				Duration:    window,
				Count:       count,
			}
			if len(concurrency) > 0 {
				opts.Concurrency = concurrency[0]
			}

			// Run against each engine.
			var results []report.EngineResult
			for _, eng := range resolved {
				r, runErr := runWorkload(cmd, wl, eng, scale, cache, opts)
				if runErr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "run: engine %s: %v\n", eng.Name(), runErr)
					continue
				}
				er := report.EngineResult{
					Name:    eng.Name(),
					Plane:   eng.Plane(),
					Version: r.Condition.EngineVersion,
					Result:  r,
				}
				results = append(results, er)

				// Publish to lineage if requested.
				if publish {
					base := lineageDir
					if base == "" {
						base = "results"
					}
					path := report.RecordPath(base, er, time.Now())
					if appendErr := report.Append(path, er); appendErr != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "run: lineage append %s: %v\n", path, appendErr)
					}
				}
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
	f.StringArrayVar(&engines, "engines", nil, "engines to run (comma-separated or repeated); default is gr (in-process)")
	f.StringVar(&scale, "scale", "SF1", "scale factor for the dataset")
	f.StringVar(&cache, "cache", "warm", "cache condition: warm|cold|both")
	f.StringVar(&format, "format", "table", "output format: table|json|markdown|csv")
	f.StringVar(&outFile, "out", "", "output file (default: stdout)")
	f.BoolVar(&publish, "publish", false, "append results to the lineage tree")
	f.StringVar(&lineageDir, "lineage-dir", "results", "lineage tree root")
	f.Float64Var(&rate, "rate", 0, "offered queries/second (0 = maximum throughput)")
	f.DurationVar(&warmup, "warmup", 5*time.Second, "warmup duration before measurement begins")
	f.DurationVar(&window, "window", 30*time.Second, "steady-state measurement window")
	f.IntVar(&count, "count", 0, "fixed query count (overrides --window if > 0)")
	f.IntSliceVar(&concurrency, "concurrency", nil, "concurrency sweep points")

	_ = cmd.MarkFlagRequired("workload")
	return cmd
}

// runWorkload runs a workload against one engine and returns the measured Result.
// This is a placeholder that returns a descriptive not-implemented error; the
// real implementation lands when the adapter interface and the measurement harness
// are fully wired in M8. The flag parsing, lineage append, and rendering all work
// now; only the actual engine execution is deferred.
func runWorkload(
	cmd *cobra.Command,
	wl *workload.Workload,
	eng Engine,
	scale, cache string,
	opts measure.Options,
) (measure.Result, error) {
	return measure.Result{}, fmt.Errorf(
		"engine execution not yet wired for %s; import the adapter package and call eng.Run()",
		eng.Name(),
	)
}

// Engine is the minimal interface the run command requires from an engine
// adapter. The full target.Target interface (with Run, Begin, Close,
// Capabilities, Version) is defined in the target package; this thin shim
// lets the CLI compile without importing it here. Adapter packages satisfy
// this interface at init() registration time.
type Engine interface {
	Name() string
	Plane() string
}

// resolveEngines resolves the engine list from the flag value. A nil or empty
// list returns the default gr in-process adapter.
func resolveEngines(names []string) ([]Engine, error) {
	// Flatten comma-separated entries.
	var flat []string
	for _, n := range names {
		for _, part := range strings.Split(n, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				flat = append(flat, part)
			}
		}
	}
	if len(flat) == 0 {
		flat = []string{"gr"}
	}
	var out []Engine
	for _, name := range flat {
		eng, ok := lookupEngine(name)
		if !ok {
			return nil, fmt.Errorf("run: unknown engine %q; run 'graph-bench list engines' to see available engines", name)
		}
		out = append(out, eng)
	}
	return out, nil
}

// engineRegistry holds the registered engine adapters. Adapter packages call
// RegisterEngine in their init() to add themselves; the run command looks them
// up here. This is a simplified in-package registry; the full target registry
// (target.Register) is the canonical one and is imported by each adapter.
var engineRegistry = map[string]Engine{}

// RegisterEngine adds an adapter to the local registry. Called from adapter
// init() functions. Panics on a duplicate name (programming error).
func RegisterEngine(e Engine) {
	if _, dup := engineRegistry[e.Name()]; dup {
		panic("graph-bench: duplicate engine registration: " + e.Name())
	}
	engineRegistry[e.Name()] = e
}

// lookupEngine returns the named engine from the registry, or false.
func lookupEngine(name string) (Engine, bool) {
	e, ok := engineRegistry[name]
	return e, ok
}
