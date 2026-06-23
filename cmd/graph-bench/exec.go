package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	grAdapter "github.com/tamnd/graph-bench/adapter/gr"
	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/measure"
	"github.com/tamnd/graph-bench/report"
	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
)

// executeRun runs a workload against a named engine and returns the EngineResult.
// It owns the full lifecycle: dataset resolve -> Setup -> Load -> measure -> Teardown.
func executeRun(
	ctx context.Context,
	engineName string,
	wl *workload.Workload,
	datasetPath string,
	datasetsDir string,
	scale string,
	cache string,
	opts measure.Options,
	lineageDir string,
	publish bool,
) (report.EngineResult, error) {
	// Resolve the target adapter.
	tgt, err := lookupTarget(engineName)
	if err != nil {
		return report.EngineResult{}, err
	}

	// Resolve the dataset.
	ds, err := resolveDataset(ctx, wl, datasetPath, datasetsDir)
	if err != nil {
		return report.EngineResult{}, fmt.Errorf("dataset: %w", err)
	}

	// Query the engine version before Setup (some adapters can do this without a DB).
	version, _ := tgt.Version(ctx)

	// Setup the engine. When the dataset is file-backed (has a directory), we
	// need a disk path for the gr bulk loader. Use a temp file for the DB.
	cfg := target.Config{}
	var dbTempDir string
	if ds.Dir() != "" && engineName == "gr" {
		tmp, err := os.MkdirTemp("", "graph-bench-db-*")
		if err != nil {
			return report.EngineResult{}, fmt.Errorf("%s: temp db dir: %w", engineName, err)
		}
		dbTempDir = tmp
		cfg.Values = map[string]string{"path": tmp + "/gr.db"}
	}
	drv, err := tgt.Setup(ctx, cfg)
	if err != nil {
		if dbTempDir != "" {
			os.RemoveAll(dbTempDir)
		}
		return report.EngineResult{}, fmt.Errorf("%s: Setup: %w", engineName, err)
	}
	defer func() {
		_ = tgt.Teardown(ctx, drv)
		if dbTempDir != "" {
			os.RemoveAll(dbTempDir)
		}
	}()

	// Load the dataset.
	loadStart := time.Now()
	loadStats, err := tgt.Load(ctx, drv, ds)
	if err != nil {
		return report.EngineResult{}, fmt.Errorf("%s: Load: %w", engineName, err)
	}
	if version == "" || version == "devel" {
		version, _ = tgt.Version(ctx)
	}
	_ = loadStart

	// Build the op schedule.
	ops, err := buildOps(wl, opts)
	if err != nil {
		return report.EngineResult{}, fmt.Errorf("build ops: %w", err)
	}
	if len(ops) == 0 {
		return report.EngineResult{}, fmt.Errorf("%s: workload %q produced no ops", engineName, wl.Name)
	}

	// Run the measurement.
	result := measure.Run(ctx, drv, ops, opts)
	result.Load = loadStats
	result.Condition = buildCondition(tgt, wl, ds, scale, cache, version, opts)

	er := report.EngineResult{
		Name:    tgt.Name(),
		Plane:   tgt.Plane().String(),
		Version: version,
		Result:  result,
	}

	// Publish to lineage if requested.
	if publish {
		base := lineageDir
		if base == "" {
			base = "results"
		}
		path := report.RecordPath(base, er, time.Now())
		if appendErr := report.Append(path, er); appendErr != nil {
			fmt.Fprintf(os.Stderr, "run: lineage append %s: %v\n", path, appendErr)
		}
	}

	return er, nil
}

// buildOps builds the measurement op slice for a workload. For workloads with a
// Mix it builds a mixed interleaved schedule; for pure isolation workloads it
// builds isolated ops for each query in sequence.
func buildOps(wl *workload.Workload, opts measure.Options) ([]measure.Op, error) {
	var ops []measure.Op
	if len(wl.Mix) > 0 {
		ops = measure.BuildMixedSchedule(wl, workload.Cypher, opts.Count, opts.Rate, opts.Warmup)
	} else {
		for _, q := range wl.Queries {
			count := opts.Count
			if count <= 0 {
				count = 100
			}
			ops = append(ops, measure.BuildIsolatedOps(q, workload.Cypher, count)...)
		}
		ops = measure.BuildSchedule(ops, opts.Rate, opts.Warmup)
	}
	return ops, nil
}

// resolveDataset finds or generates the dataset for the workload. Priority:
// 1. --dataset-path is an explicit path to an existing dataset directory.
// 2. Look for a matching directory in --datasets-dir (default "datasets/").
// 3. Auto-generate a small synthetic dataset for known synthetic workload kinds.
func resolveDataset(ctx context.Context, wl *workload.Workload, datasetPath, datasetsDir string) (target.Dataset, error) {
	// Explicit path wins.
	if datasetPath != "" {
		ds, err := dataset.Open(datasetPath)
		if err != nil {
			return nil, fmt.Errorf("open dataset at %s: %w", datasetPath, err)
		}
		return ds, nil
	}

	dsName := wl.Dataset
	if dsName == "" {
		// Workload needs no dataset (e.g., writes that build their own graph).
		return target.NewStatements(wl.Name+"-empty", nil), nil
	}

	// Search in datasetsDir for a directory whose manifest.Name matches.
	if datasetsDir != "" {
		if ds, err := findDataset(datasetsDir, dsName); err == nil {
			return ds, nil
		}
	}

	// Auto-generate a synthetic dataset from the workload's dataset name.
	ds, err := autoGenDataset(ctx, dsName)
	if err != nil {
		return nil, fmt.Errorf("cannot find or generate dataset %q: %w; run 'graph-bench generate' first or use --dataset-path", dsName, err)
	}
	return ds, nil
}

// findDataset scans dir for a subdirectory whose manifest.Name matches name.
func findDataset(dir, name string) (*dataset.Set, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := dir + "/" + e.Name()
		ds, err := dataset.Open(path)
		if err != nil {
			continue
		}
		m := ds.Manifest()
		if m != nil && m.Name == name {
			return ds, nil
		}
	}
	return nil, fmt.Errorf("no dataset named %q in %s", name, dir)
}

// autoGenDataset generates a small deterministic synthetic dataset for known
// workload dataset names. Used when no explicit path is given and the dataset
// is not found in the datasets directory. Writes to a temp directory.
func autoGenDataset(ctx context.Context, dsName string) (*dataset.Set, error) {
	cfg, ok := syntheticDefaults[dsName]
	if !ok {
		return nil, fmt.Errorf("unknown dataset name %q", dsName)
	}
	dir, err := os.MkdirTemp("", "graph-bench-ds-*")
	if err != nil {
		return nil, err
	}
	w, err := dataset.NewWriter(dir)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	_, err = gen.Generate(ctx, cfg, w)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return ds, nil
}

// syntheticDefaults maps workload dataset names to a small but meaningful
// auto-generation config for the run command's auto-gen path. These are
// not the benchmark-grade sizes; they exist so 'graph-bench run' works
// without a pre-generated dataset for workloads that say what they need.
var syntheticDefaults = map[string]gen.Config{
	"grid": {Kind: "grid", Rows: 100, Cols: 100, Seed: 1},
	"er":   {Kind: "er", N: 10000, P: 0.001, Seed: 1},
	"rmat": {Kind: "rmat", Scale: 14, EdgeFactor: 16, Seed: 1},
}

// buildCondition fills in the Condition stamp for a result.
func buildCondition(
	tgt target.Target,
	wl *workload.Workload,
	ds target.Dataset,
	scale, cache, version string,
	opts measure.Options,
) measure.Condition {
	c := measure.Condition{
		Engine:          tgt.Name(),
		EngineVersion:   version,
		Plane:           tgt.Plane().String(),
		Dataset:         ds.Name(),
		DatasetChecksum: ds.Checksum(),
		Workload:        wl.Name,
		Scale:           scale,
		Cache:           cache,
		OfferedRate:     opts.Rate,
		GoVersion:       runtime.Version(),
		OS:              runtime.GOOS + "/" + runtime.GOARCH,
		Timestamp:       time.Now().UTC(),
	}
	if opts.Count > 0 {
		c.Repetitions = opts.Count
	}
	return c
}

// targetRegistry holds registered target adapters. The gr adapter is always
// registered. Bolt adapters are registered when built with -tags bolt.
var targetRegistry = map[string]target.Target{}

func init() {
	registerTarget(grAdapter.New())
}

// registerTarget adds a target adapter to the registry. Called from init
// functions in adapter packages. Panics on duplicate names (programming error).
func registerTarget(t target.Target) {
	if _, dup := targetRegistry[t.Name()]; dup {
		panic("graph-bench: duplicate target registration: " + t.Name())
	}
	targetRegistry[t.Name()] = t
}

// lookupTarget returns the named target adapter from the registry.
func lookupTarget(name string) (target.Target, error) {
	t, ok := targetRegistry[name]
	if !ok {
		return nil, fmt.Errorf("unknown engine %q; use 'graph-bench list engines' to see available engines", name)
	}
	return t, nil
}
