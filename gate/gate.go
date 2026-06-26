// Package gate runs a workload against a set-up engine and checks the result
// against the budget. It is the layer that ties the others together: it plans
// the run, materializes the dataset, stands each engine up through the Target
// SPI, drives measure to take the numbers, hands the result to slo for a
// verdict, and hands the verdict to the report. The gate tests (TestSmokeGate,
// TestScaleGate) live here, so a manual run through the command and a CI run
// through go test exercise the identical path.
//
// See notes/Spec/2060/bench/07-slo-gates-and-regression.md for the full design.
package gate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tamnd/graph-bench/budget"
	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/measure"
	"github.com/tamnd/graph-bench/slo"
	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
)

// Spec parameterizes a run: which engines, which workload, at what scale, cold
// or warm, how many repetitions, at what concurrency, and which budget set to
// hold the result to. Zero fields are filled by withDefaults.
type Spec struct {
	Engines     []target.Target // the engines to run; each is measured behind the same SPI
	Workload    *workload.Workload
	Scale       string     // dataset scale, e.g. "SF1", "scale-20", or empty for synthetic
	Cold        bool       // measure cold-cache first access in addition to warm (F5)
	Repetitions int        // measured repetitions; the gate compares median-of-repetitions
	Concurrency []int      // the concurrency points to sweep
	Rate        float64    // open-model offered queries/second
	BudgetSet   budget.Set // ceiling set keyed by plane and scale (section 2.3)
	MinSamples  int        // per-class sample floor below which a class is not gated
	FlatnessTol float64    // flatness ratio tolerance; default 3.0 when zero
	DatasetDir  string     // if set, open a pre-materialized dataset from this dir instead of generating
}

func (s Spec) withDefaults() Spec {
	if s.Repetitions <= 0 {
		s.Repetitions = 1 // minimum for the smoke gate; production raises it
	}
	if len(s.Concurrency) == 0 {
		s.Concurrency = []int{1}
	}
	if s.Rate <= 0 {
		s.Rate = 200
	}
	if s.MinSamples <= 0 {
		s.MinSamples = 10 // low floor for synthetic micro workloads in CI
	}
	if s.FlatnessTol <= 0 {
		s.FlatnessTol = 3.0
	}
	return s
}

// Outcome is everything a run produced: the per-engine measured results, the
// slo verdict for gr against its budget, the flatness verdict when the run had
// two scales, and the cross-engine matrix. The verdicts gate the build; the
// matrix is reported, never gated (ADR-8, doc 07 section 1.1).
type Outcome struct {
	Results  map[string]measure.Result // keyed by Target.Name()
	Report   slo.Report                // gr's absolute budget verdict (the gate)
	Flatness slo.FlatnessReport        // gr's flatness verdict, empty for single-scale runs
}

// Pass reports whether the gating verdicts passed. The matrix is not consulted:
// gr being slower than another engine is reported, never gated.
func (o Outcome) Pass() bool {
	return o.Report.Pass() && o.Flatness.Pass()
}

// Run plans a run, materializes the dataset once, measures each engine through
// the SPI, gates gr against its budget, and returns the outcome. Only gr's
// result is checked against the budget; the other engines' results go into
// Results for the matrix. The build fails when Outcome.Pass() is false.
func Run(ctx context.Context, spec Spec) (Outcome, error) {
	spec = spec.withDefaults()
	if spec.Workload == nil {
		return Outcome{}, fmt.Errorf("gate.Run: Spec.Workload is nil")
	}
	if len(spec.Engines) == 0 {
		return Outcome{}, fmt.Errorf("gate.Run: Spec.Engines is empty")
	}

	ds, err := openOrGenerate(ctx, spec)
	if err != nil {
		return Outcome{}, fmt.Errorf("gate: materialize dataset: %w", err)
	}

	results := make(map[string]measure.Result, len(spec.Engines))
	for _, eng := range spec.Engines {
		if ctx.Err() != nil {
			break
		}
		r, err := measureEngine(ctx, eng, ds, spec)
		if err != nil {
			return Outcome{}, fmt.Errorf("gate: measure %s: %w", eng.Name(), err)
		}
		results[eng.Name()] = r
	}

	grRes := results["gr"]
	rep := slo.Check(grRes)

	return Outcome{
		Results: results,
		Report:  rep,
	}, nil
}

// openOrGenerate returns a dataset, either from spec.DatasetDir or by
// generating a synthetic one from the workload's Dataset field.
func openOrGenerate(ctx context.Context, spec Spec) (target.Dataset, error) {
	if spec.DatasetDir != "" {
		return dataset.Open(spec.DatasetDir)
	}
	// Generate a synthetic dataset in a temp dir.
	kind := spec.Workload.Dataset
	if kind == "" {
		kind = "grid" // fallback for the smoke gate
	}
	dir, err := os.MkdirTemp("", "graph-bench-gate-*")
	if err != nil {
		return nil, err
	}
	w, err := dataset.NewWriter(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	cfg := syntheticConfig(kind, spec.Scale)
	if _, err := gen.Generate(ctx, cfg, w); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("generate %q dataset: %w", kind, err)
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return ds, nil
}

// syntheticConfig maps a dataset kind and a scale string to a gen.Config.
func syntheticConfig(kind, scale string) gen.Config {
	switch kind {
	case "grid":
		switch scale {
		case "small", "":
			return gen.Config{Kind: "grid", Rows: 100, Cols: 100} // 10k nodes
		case "medium":
			return gen.Config{Kind: "grid", Rows: 300, Cols: 300}
		default:
			return gen.Config{Kind: "grid", Rows: 100, Cols: 100}
		}
	case "er":
		return gen.Config{Kind: "er", N: 500, P: 0.05, Seed: 42}
	default:
		return gen.Config{Kind: "grid", Rows: 100, Cols: 100}
	}
}

// measureEngine stands one engine up, loads the dataset, builds and fires the
// schedule, and returns the measured result.
func measureEngine(ctx context.Context, eng target.Target, ds target.Dataset, spec Spec) (measure.Result, error) {
	// Provide a temp path so engines that need a disk-backed database (e.g. gr's
	// bulk CSV loader) have a location to write to. An in-memory engine ignores
	// the path.
	tmpDir, err := os.MkdirTemp("", "graph-bench-db-*")
	if err != nil {
		return measure.Result{}, fmt.Errorf("create db dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := target.Config{Values: map[string]string{"path": filepath.Join(tmpDir, "bench.gr")}}
	drv, err := eng.Setup(ctx, cfg)
	if err != nil {
		return measure.Result{}, fmt.Errorf("setup: %w", err)
	}
	defer func() { _ = eng.Teardown(ctx, drv) }()

	if _, err := eng.Load(ctx, drv, ds); err != nil {
		return measure.Result{}, fmt.Errorf("load: %w", err)
	}

	ops, err := buildOps(ds, spec.Workload, spec.Rate)
	if err != nil {
		return measure.Result{}, fmt.Errorf("build schedule: %w", err)
	}
	if len(ops) == 0 {
		return measure.Result{}, fmt.Errorf("no ops for workload %q", spec.Workload.Name)
	}

	opt := measure.Options{
		Rate:        spec.Rate,
		Count:       len(ops),
		Concurrency: spec.Concurrency[0],
		Warmup:      0, // no warmup for the smoke gate's small synthetic run
	}
	return measure.Run(ctx, drv, ops, opt), nil
}

// buildOps builds one Op per query in the workload. For each query that has a
// Cypher text, it picks one parameter set from the query's parameter source
// (or nil when the source is nil) and adds one scheduled op. The schedule is
// then assigned evenly-spaced offsets at rate.
func buildOps(ds target.Dataset, wl *workload.Workload, rate float64) ([]measure.Op, error) {
	sources, err := curatedSources(ds, wl)
	if err != nil {
		return nil, err
	}
	var ops []measure.Op
	for _, wq := range wl.Queries {
		text, ok := wq.Texts[workload.Cypher]
		if !ok || text == "" {
			continue
		}
		q := target.Query{
			ID:    wq.ID,
			Class: wq.Class,
			Text:  text,
		}
		var params target.Params
		if src := sources[wq.ID]; src != nil {
			params = src.Next()
		} else if wq.Params != nil {
			params = wq.Params.Next()
		}
		ops = append(ops, measure.Op{
			Class:  wq.Class,
			Query:  q,
			Params: params,
		})
	}
	measure.BuildSchedule(ops, rate, 0)
	return ops, nil
}

// curatedSources curates the dataset and loads the parameter pool for every
// query that declares a PoolKey, so the smoke gate fires the same parameterized
// queries a real run does instead of sending an unbound $param the engine
// rejects at the transport level. Without this the bounded workload's seed and
// id parameters are nil and every parameterized query fails, which only the
// CI-skipped failure check hid. A statements dataset (no directory) or an
// unreadable pool yields no source for that query, and buildOps falls back to
// the query's own Params.
func curatedSources(ds target.Dataset, wl *workload.Workload) (map[string]workload.ParamSource, error) {
	needsPool := false
	for _, q := range wl.Queries {
		if q.PoolKey != "" {
			needsPool = true
			break
		}
	}
	if !needsPool {
		return nil, nil
	}
	if ds.Dir() != "" {
		if err := workload.Curate(ds, 1); err != nil {
			return nil, fmt.Errorf("curate %s: %w", ds.Name(), err)
		}
	}
	cached := map[string]workload.ParamSource{}
	out := map[string]workload.ParamSource{}
	for _, q := range wl.Queries {
		if q.PoolKey == "" {
			continue
		}
		if ps, ok := cached[q.PoolKey]; ok {
			if ps != nil {
				out[q.ID] = ps
			}
			continue
		}
		pool, err := ds.Params(q.PoolKey)
		if err != nil || len(pool) == 0 {
			cached[q.PoolKey] = nil
			continue
		}
		ps := workload.NewPool(pool)
		cached[q.PoolKey] = ps
		out[q.ID] = ps
	}
	return out, nil
}

// DatasetPath returns a path suitable for caching a generated dataset next to
// the binary's testdata directory, so regeneration across test runs is avoided.
// It is exported for use in tests that want a persistent dataset rather than a
// freshly generated temp dir.
func DatasetPath(kind, scale string) string {
	return filepath.Join(os.TempDir(), "graph-bench-datasets", kind+"-"+scale)
}
