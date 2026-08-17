package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/report"
	"github.com/tamnd/graph-bench/workload"
)

// profile is the run protocol preset. The contract (spec 09 §1): "fast" keeps
// each workload×engine under about a minute of wall clock including load —
// small counts, fixed short warmup, no sweep, no cold pass, two-sample
// validation. "full" follows the spec protocol: fixed-fraction warmup, the CI
// concurrency sweep on mixed workloads, a cold pass on persistent engines,
// five analytics repetitions with the first discarded, default validation.
type profile struct {
	name          string
	counts        map[engine.Class]int // per-class query counts (count mode)
	mixCount      int                  // total ops for a mixed schedule
	analyticsReps int
	discardFirst  bool
	fixedWarmup   time.Duration // >0: fixed wall-clock warmup, detector skipped
	sweep         bool          // concurrency sweep (CISweepPoints) on mixed workloads
	cold          bool          // cold pass on persistent engines
	sampleDefault int           // verify.Options.SampleDefault (0 = package default 4)

	// budget caps the wall clock spent per query in count mode (0 = no cap).
	// It is what actually holds "fast" to its promise: the per-class counts
	// bound the number of repetitions, but not their cost, and one windowed
	// depth-4 path enumeration repeated a hundred times outruns the whole
	// rest of the sweep. Only "fast" sets it — a truncated sample is a fine
	// preview and a bad headline number.
	budget time.Duration
}

// countFor returns the per-class query count, ported from v1's per-class
// defaults: cheap classes run often, expensive classes run enough for a p99.
func (p profile) countFor(cl engine.Class) int {
	if n, ok := p.counts[cl]; ok {
		return n
	}
	return p.counts[engine.PointRead]
}

var profiles = map[string]profile{
	"fast": {
		name: "fast",
		counts: map[engine.Class]int{
			engine.PointRead:   100,
			engine.Traversal:   100,
			engine.Subgraph:    50,
			engine.Aggregation: 20,
			engine.Analytical:  5,
			engine.Write:       100,
		},
		mixCount:      200,
		analyticsReps: 2,
		discardFirst:  false,
		fixedWarmup:   2 * time.Second,
		sweep:         false,
		cold:          false,
		sampleDefault: 2,
		budget:        2 * time.Second,
	},
	"full": {
		name: "full",
		counts: map[engine.Class]int{
			engine.PointRead:   1000,
			engine.Traversal:   1000,
			engine.Subgraph:    500,
			engine.Aggregation: 100,
			engine.Analytical:  10,
			engine.Write:       1000,
		},
		mixCount:      2000,
		analyticsReps: 5,
		discardFirst:  true,
		fixedWarmup:   0, // fixed-fraction warmup with the 08 §3 floors
		sweep:         true,
		cold:          true,
		sampleDefault: 0, // package default (4)
		budget:        0, // no truncation: these are the reportable numbers
	},
}

// profileFor resolves a --profile flag value.
func profileFor(name string) (profile, error) {
	p, ok := profiles[name]
	if !ok {
		return profile{}, fmt.Errorf("unknown profile %q; use fast or full", name)
	}
	return p, nil
}

// newRunCmd builds the run verb: the full pipeline of spec 08 §5 per engine,
// with the verification table printed before any timing output.
func newRunCmd() *cobra.Command {
	var (
		wlName      string
		only        []string
		engines     []string
		profileName string
		scale       string
		count       int
		rate        float64
		concurrency int
		outDir      string
		publish     bool
		tuned       bool
		noDocker    bool
		dsDir       string
		seed        int64
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run a workload against one or more engines and record the measurement",
		Long: "run executes a named workload (--workload) against one or more engines " +
			"(--engines, comma-separated or repeated) through the full pipeline: dataset " +
			"resolve, Start, Load, parameter binding, verification (printed before any " +
			"timing), warmup, measurement, Condition stamp. Each engine's result is " +
			"written as a schema-3 document under --out and rendered as a matrix. " +
			"--profile fast keeps a run under a minute; --profile full follows the spec " +
			"protocol (sweep, cold pass, full validation). --publish updates the lineage " +
			"index. Bolt engines without a configured server get a managed Docker " +
			"container unless --no-docker or GRAPH_BENCH_SKIP_DOCKER is set.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if wlName == "" {
				return fmt.Errorf("run: --workload is required")
			}
			wl, err := workload.Lookup(wlName)
			if err != nil {
				return fmt.Errorf("run: %w; run 'graph-bench list workloads'", err)
			}
			prof, err := profileFor(profileName)
			if err != nil {
				return fmt.Errorf("run: %w", err)
			}
			switch strings.ToLower(scale) {
			case "smoke", "sf1":
			default:
				return fmt.Errorf("run: --scale must be smoke or sf1, got %q", scale)
			}
			flat := flattenEngines(engines)
			if len(flat) == 0 {
				flat = []string{"zu"}
			}
			focused := flattenEngines(only)
			if len(focused) > 0 {
				if publish {
					return fmt.Errorf("run: --query is a development probe, it cannot --publish")
				}
				wl, err = focus(wl, focused)
				if err != nil {
					return fmt.Errorf("run: %w", err)
				}
			}

			rc := runConfig{
				profile:     prof,
				scale:       strings.ToLower(scale),
				count:       count,
				rate:        rate,
				concurrency: concurrency,
				outDir:      outDir,
				publish:     publish,
				tuned:       tuned,
				datasetsDir: datasetsDir(dsDir),
				seed:        seed,
				noDocker:    noDocker,
				stderr:      cmd.ErrOrStderr(),
				stdout:      cmd.OutOrStdout(),
			}

			var docs []*report.Document
			failed := 0
			for _, eng := range flat {
				doc, runErr := executeRun(cmd.Context(), eng, wl, rc)
				if runErr != nil {
					failed++
					fmt.Fprintf(cmd.ErrOrStderr(), "run: engine %s: %v\n", eng, runErr)
					continue
				}
				if len(focused) > 0 {
					fmt.Fprintf(cmd.OutOrStdout(),
						"focused on %s, no result written\n", strings.Join(focused, ","))
				} else if path, werr := report.Write(outDir, doc); werr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "run: write result: %v\n", werr)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", path)
				}
				docs = append(docs, doc)
			}
			if len(docs) == 0 {
				return fmt.Errorf("run: all engines failed or produced no result")
			}
			if publish {
				if err := report.UpdateIndex(outDir); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "run: update index: %v\n", err)
				}
			}

			fmt.Fprintln(cmd.OutOrStdout())
			report.RenderTable(cmd.OutOrStdout(), report.NewMatrix(docs))
			if failed > 0 {
				return fmt.Errorf("run: %d engine(s) failed", failed)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&wlName, "workload", "", "workload name (required); see 'list workloads'")
	f.StringArrayVar(&only, "query", nil,
		"run only these query ids (comma-separated or repeated); a development probe, writes no result")
	f.StringArrayVar(&engines, "engines", nil, "engines to run (comma-separated or repeated); default zu")
	f.StringVar(&profileName, "profile", "fast", "run protocol preset: fast|full")
	f.StringVar(&scale, "scale", "smoke", "dataset scale tier: smoke|sf1")
	f.IntVar(&count, "count", 0, "per-query count override (0 = profile default)")
	f.Float64Var(&rate, "rate", 0, "offered queries/second for mixed workloads (0 = count mode, service time)")
	f.IntVar(&concurrency, "concurrency", 1, "worker pool size for the measured run")
	f.StringVar(&outDir, "out", "results", "lineage directory results are written under")
	f.BoolVar(&publish, "publish", false, "update the lineage INDEX.md after writing")
	f.BoolVar(&tuned, "tuned", false, "mark this run as tuned (never compared silently with untuned)")
	f.BoolVar(&noDocker, "no-docker", false, "never start managed containers (also: GRAPH_BENCH_SKIP_DOCKER)")
	f.StringVar(&dsDir, "datasets-dir", "", "datasets directory (default $GRAPH_BENCH_DATA or ./datasets)")
	f.Int64Var(&seed, "seed", 1, "PRNG seed for curation and the mixed schedule")

	_ = cmd.MarkFlagRequired("workload")
	return cmd
}

// focus returns a copy of wl holding only the named queries, in the
// workload's own order. It is for the loop a person runs while they are
// changing one operator, where waiting out the other eight queries is the
// whole cost of the iteration.
//
// A run this narrow is not a measurement of the workload, so the caller
// writes no result document for it and --publish is refused outright. The
// mixed workloads are refused here too: a mix is defined by its weights,
// and a subset of the queries is a different blend rather than a smaller
// sample of the same one.
func focus(wl *workload.Workload, ids []string) (*workload.Workload, error) {
	if wl.Mix != nil {
		return nil, fmt.Errorf("--query on %q, which is a mixed workload, and a subset of a mix is a different mix", wl.Name)
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	out := *wl
	out.Queries = nil
	for _, q := range wl.Queries {
		if want[q.ID] {
			out.Queries = append(out.Queries, q)
			delete(want, q.ID)
		}
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for id := range want {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		have := make([]string, 0, len(wl.Queries))
		for _, q := range wl.Queries {
			have = append(have, q.ID)
		}
		return nil, fmt.Errorf("workload %q has no query %s; it has %s",
			wl.Name, strings.Join(missing, ", "), strings.Join(have, ", "))
	}
	return &out, nil
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
