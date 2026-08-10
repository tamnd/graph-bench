// The named dataset recipe table: every Workload.Dataset name maps either to
// a dataset/gen config (generated on demand, checksum-verified, reused) or to
// an LDBC pin (fetched and verified via dataset/ldbc). The smoke table maps a
// base recipe to its ~1/10-scale variant so `--scale smoke` keeps a full
// workload×engine run under a minute (spec 09 §1, profile contract).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/dataset/ldbc"
	"github.com/tamnd/graph-bench/engine"
)

// recipes maps a dataset name to its deterministic generator config. Seeds
// are fixed: the recipe name identifies the bytes (bit-reproducibility,
// spec 05 §2).
var recipes = map[string]gen.Config{
	// Benchmark-grade bases.
	"grid-100x100": {Kind: "grid", Rows: 100, Cols: 100, Seed: 1},
	"er-10k":       {Kind: "er", N: 10000, P: 0.001, Seed: 1},
	"uniform-10k":  {Kind: "uniform", N: 10000, Degree: 16, Seed: 1},
	"powerlaw-10k": {Kind: "powerlaw", N: 10000, Gamma: 2.5, MinDeg: 1, MaxDeg: 500, Seed: 1},
	"rmat-14":      {Kind: "rmat", Scale: 14, EdgeFactor: 16, Seed: 1},
	"rmat-14-w":    {Kind: "rmat", Scale: 14, EdgeFactor: 16, Weighted: true, Seed: 1},
	"urand-14":     {Kind: "urand", Scale: 14, EdgeFactor: 16, Seed: 1},
	"fin-10k":      {Kind: "fin", Accounts: 10000, Seed: 1},
	"lb-10k":       {Kind: "lb", N: 10000, Seed: 1},
	"social-1k":    {Kind: "social", Persons: 1000, Seed: 1},

	// Smoke variants at roughly 1/10 scale.
	"grid-30x30":  {Kind: "grid", Rows: 30, Cols: 30, Seed: 1},
	"er-1k":       {Kind: "er", N: 1000, P: 0.01, Seed: 1},
	"uniform-1k":  {Kind: "uniform", N: 1000, Degree: 16, Seed: 1},
	"powerlaw-1k": {Kind: "powerlaw", N: 1000, Gamma: 2.5, MinDeg: 1, MaxDeg: 100, Seed: 1},
	"rmat-10":     {Kind: "rmat", Scale: 10, EdgeFactor: 16, Seed: 1},
	"rmat-10-w":   {Kind: "rmat", Scale: 10, EdgeFactor: 16, Weighted: true, Seed: 1},
	"urand-10":    {Kind: "urand", Scale: 10, EdgeFactor: 16, Seed: 1},
	"fin-1k":      {Kind: "fin", Accounts: 1000, Seed: 1},
	"lb-1k":       {Kind: "lb", N: 1000, Seed: 1},
	"social-200":  {Kind: "social", Persons: 200, Seed: 1},
}

// smokeVariant maps a base recipe (or pin) name to its smoke-scale variant.
// Names without an entry pass through unchanged (`--scale sf1` keeps bases).
var smokeVariant = map[string]string{
	"grid-100x100": "grid-30x30",
	"er-10k":       "er-1k",
	"uniform-10k":  "uniform-1k",
	"powerlaw-10k": "powerlaw-1k",
	"rmat-14":      "rmat-10",
	"rmat-14-w":    "rmat-10-w",
	"urand-14":     "urand-10",
	"fin-10k":      "fin-1k",
	"lb-10k":       "lb-1k",
	"social-1k":    "social-200",
	// snb-sf1 has no generated smoke twin; the SNB-shaped synthetic stands in.
	"snb-sf1": "social-200",
}

// pinNames maps a dataset name to the LDBC pin scale label it resolves
// through (dataset/ldbc.LoadPin + Fetch).
var pinNames = map[string]string{
	"snb-sf1": "sf1",
}

// resolveRecipeName applies the scale flag to a dataset name: "smoke" maps a
// base name to its smoke variant (table-driven), anything else keeps the base.
func resolveRecipeName(name, scaleFlag string) string {
	if strings.EqualFold(scaleFlag, "smoke") {
		if smoke, ok := smokeVariant[name]; ok {
			return smoke
		}
	}
	return name
}

// datasetsDir returns the directory generated datasets live in:
// $GRAPH_BENCH_DATA, default ./datasets (spec 09 §4).
func datasetsDir(flag string) string {
	if flag != "" {
		return flag
	}
	if d := os.Getenv("GRAPH_BENCH_DATA"); d != "" {
		return d
	}
	return "datasets"
}

// resolveDataset finds or materializes the dataset for name at the given
// scale. Pin names fetch through dataset/ldbc; recipe names generate on
// demand into dir, reusing an existing directory when its checksum verifies.
func resolveDataset(ctx context.Context, name, scaleFlag, dir string) (engine.Dataset, error) {
	if name == "" {
		return engine.NewStatements("empty", engine.Schema{}, nil), nil
	}
	name = resolveRecipeName(name, scaleFlag)

	if scale, ok := pinNames[name]; ok {
		pin, err := ldbc.LoadPin(scale)
		if err != nil {
			return nil, fmt.Errorf("pin %s: %w", name, err)
		}
		return ldbc.Fetch(ctx, pin, &ldbc.FetchOptions{
			CacheDir: filepath.Join(dir, "ldbc"),
		})
	}

	cfg, ok := recipes[name]
	if !ok {
		// Not a recipe: fall back to an existing materialized directory whose
		// manifest name matches, so pre-generated custom datasets still work.
		if ds, err := findDataset(dir, name); err == nil {
			return ds, nil
		}
		return nil, fmt.Errorf("unknown dataset %q; see 'graph-bench list datasets'", name)
	}
	return materializeRecipe(ctx, name, cfg, dir)
}

// recipeIndexFile records recipe name -> materialized directory, so a resolve
// after the first is a checksum-verified open, not a regeneration.
const recipeIndexFile = ".recipes.json"

func readRecipeIndex(dir string) map[string]string {
	idx := map[string]string{}
	data, err := os.ReadFile(filepath.Join(dir, recipeIndexFile))
	if err == nil {
		_ = json.Unmarshal(data, &idx)
	}
	return idx
}

func writeRecipeIndex(dir string, idx map[string]string) {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, recipeIndexFile), append(data, '\n'), 0o644)
}

// materializeRecipe returns the dataset for a recipe, generating it when the
// cached copy is absent or fails checksum verification. The final directory
// is <dir>/<manifest-name>-<checksum8> (dataset.DirName), so identical
// recipes land on identical paths on every machine.
func materializeRecipe(ctx context.Context, name string, cfg gen.Config, dir string) (*dataset.Set, error) {
	idx := readRecipeIndex(dir)
	if sub, ok := idx[name]; ok {
		if ds, err := dataset.Open(filepath.Join(dir, sub)); err == nil {
			return ds, nil
		}
		// Stale or corrupt: fall through and regenerate.
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(dir, ".gen-")
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(stage)
		}
	}()

	w, err := dataset.NewWriter(stage)
	if err != nil {
		return nil, err
	}
	m, err := gen.Generate(ctx, cfg, w)
	if err != nil {
		return nil, fmt.Errorf("generate %s: %w", name, err)
	}

	final := filepath.Join(dir, dataset.DirName(m))
	if _, statErr := os.Stat(final); statErr != nil {
		if err := os.Rename(stage, final); err != nil {
			return nil, err
		}
		keep = true
	}
	ds, err := dataset.Open(final)
	if err != nil {
		return nil, fmt.Errorf("open generated %s: %w", name, err)
	}
	idx[name] = filepath.Base(final)
	writeRecipeIndex(dir, idx)
	return ds, nil
}

// findDataset scans dir for a subdirectory whose manifest name matches name
// and whose checksum verifies (dataset.Open verifies).
func findDataset(dir, name string) (*dataset.Set, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		ds, err := dataset.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if m := ds.Manifest(); m != nil && m.Name == name {
			return ds, nil
		}
	}
	return nil, fmt.Errorf("no dataset named %q in %s", name, dir)
}
