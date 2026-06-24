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
	curateSeed int64,
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
	if ds.Dir() != "" && tgt.Plane() == target.InProc {
		tmp, err := os.MkdirTemp("", "graph-bench-db-*")
		if err != nil {
			return report.EngineResult{}, fmt.Errorf("%s: temp db dir: %w", engineName, err)
		}
		dbTempDir = tmp
		cfg.Values = map[string]string{"path": tmp + "/graph-bench.db"}
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

	// Load curated parameter pools for any query that declares a PoolKey.
	// Auto-curates params.json if absent and the dataset has a directory.
	paramSources, err := loadParamSources(ctx, wl, ds, curateSeed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: load curated params: %v (queries will run without seeded params)\n", err)
		paramSources = nil
	}

	// Build the op schedule.
	ops, err := buildOps(wl, engineName, opts, paramSources)
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

// buildOps builds the measurement op slice for a workload. paramSources overrides
// each query's Params field by query ID; a nil map falls back to q.Params.
func buildOps(wl *workload.Workload, engineName string, opts measure.Options, paramSources map[string]workload.ParamSource) ([]measure.Op, error) {
	d := dialectFor(engineName)
	var ops []measure.Op
	if len(wl.Mix) > 0 {
		ops = measure.BuildMixedSchedule(wl, d, opts.Count, opts.Rate, opts.Warmup)
	} else {
		for _, q := range wl.Queries {
			count := opts.Count
			if count <= 0 {
				count = 100
			}
			ps := paramSources[q.ID]
			if ps == nil {
				ps = q.Params
			}
			ops = append(ops, buildQueryOps(q, d, count, ps)...)
		}
		ops = measure.BuildSchedule(ops, opts.Rate, opts.Warmup)
	}
	return ops, nil
}

// dialectFor maps an engine name to the workload dialect it speaks. Most engines
// speak standard openCypher; Kuzu-family engines (ladybug) speak a Kuzu variant
// that differs in a few constructs (notably shortestPath is not supported).
func dialectFor(engineName string) workload.Dialect {
	switch engineName {
	case "ladybug":
		return workload.KuzuCypher
	default:
		return workload.Cypher
	}
}

// buildQueryOps builds count isolated ops for one query, drawing params from ps.
// It mirrors BuildIsolatedOps but accepts an external ParamSource so the caller
// can supply the curated pool without mutating the global WorkloadQuery.
func buildQueryOps(q *workload.WorkloadQuery, d workload.Dialect, count int, ps workload.ParamSource) []measure.Op {
	if count <= 0 {
		return nil
	}
	query, _, ok := q.Resolve(d, nil)
	if !ok {
		// Fall back to Cypher if the engine's preferred dialect has no text for this query.
		query, _, ok = q.Resolve(workload.Cypher, nil)
		if !ok {
			return nil
		}
	}
	ops := make([]measure.Op, 0, count)
	for i := 0; i < count; i++ {
		var params target.Params
		if ps != nil {
			params = ps.Next()
		}
		ops = append(ops, measure.Op{
			Class:   q.Class,
			QueryID: q.ID,
			Query:   query,
			Params:  params,
		})
	}
	return ops
}

// loadParamSources loads the curated parameter pools for all queries in the
// workload that declare a PoolKey. If the dataset has a directory but no
// params.json, it runs workload.Curate first (idempotent). Returns a map from
// query ID to ParamSource; missing or unreadable pools produce no entry.
func loadParamSources(ctx context.Context, wl *workload.Workload, ds target.Dataset, curateSeed int64) (map[string]workload.ParamSource, error) {
	_ = ctx
	// Check if any query needs a curated pool.
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

	// Auto-curate if the dataset has a directory (file-backed, not a statements set).
	if ds.Dir() != "" {
		if err := workload.Curate(ds, curateSeed); err != nil {
			return nil, fmt.Errorf("curate %s: %w", ds.Name(), err)
		}
	}

	// Load each query's pool, caching by PoolKey so each JSON read happens once.
	cachedPools := map[string]workload.ParamSource{}
	result := map[string]workload.ParamSource{}
	for _, q := range wl.Queries {
		if q.PoolKey == "" {
			continue
		}
		if ps, ok := cachedPools[q.PoolKey]; ok {
			if ps != nil {
				result[q.ID] = ps
			}
			continue
		}
		pool, err := ds.Params(q.PoolKey)
		if err != nil || len(pool) == 0 {
			cachedPools[q.PoolKey] = nil
			continue
		}
		ps := workload.NewPool(pool)
		cachedPools[q.PoolKey] = ps
		result[q.ID] = ps
	}
	return result, nil
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
	"grid":     {Kind: "grid", Rows: 100, Cols: 100, Seed: 1},
	"er":       {Kind: "er", N: 10000, P: 0.001, Seed: 1},
	"rmat":     {Kind: "rmat", Scale: 14, EdgeFactor: 16, Seed: 1},
	"powerlaw": {Kind: "powerlaw", N: 5000, Gamma: 2.5, MinDeg: 1, MaxDeg: 500, Seed: 1},
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
