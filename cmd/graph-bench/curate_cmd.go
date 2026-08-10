// Parameter curation: deterministic sampling of node identifiers from the
// canonical CSV into params.json pools, so every engine draws the identical
// parameter sequence (ADR-8). The pool semantics live in workload.Curate;
// this file is the CLI verb around it plus a name-shaped fallback for keys
// curation does not know.
package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

// curatePoolSize is the default draw count per pool.
const curatePoolSize = 64

// newCurateCmd builds the curate verb: it pre-computes parameter pools for a
// materialized dataset so the first benchmark run pays no curation cost.
func newCurateCmd() *cobra.Command {
	var (
		dsPath string
		seed   int64
		size   int
		keys   []string
	)
	cmd := &cobra.Command{
		Use:   "curate",
		Short: "Pre-compute curated parameter pools for a dataset",
		Long: "curate reads a materialized dataset directory and writes params.json " +
			"beside manifest.json with deterministically sampled parameter pools. " +
			"Pool keys default to every PoolKey declared by registered workloads; " +
			"--keys overrides. Curation is idempotent: the same seed produces the " +
			"same pools.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dsPath == "" {
				return fmt.Errorf("curate: --dataset is required")
			}
			ds, err := dataset.Open(dsPath)
			if err != nil {
				return fmt.Errorf("curate: open dataset %s: %w", dsPath, err)
			}
			want := flattenEngines(keys)
			if len(want) == 0 {
				want = registeredPoolKeys()
			}
			if len(want) == 0 {
				return fmt.Errorf("curate: no pool keys: no workloads registered and --keys empty")
			}
			if err := curatePools(ds, want, size, seed); err != nil {
				return fmt.Errorf("curate: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "curated %s: %d pool(s), seed=%d, size=%d\n",
				dsPath, len(want), seed, size)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&dsPath, "dataset", "", "path to a materialized dataset directory (required)")
	f.Int64Var(&seed, "seed", 1, "PRNG seed for sampling; the only source of randomness")
	f.IntVar(&size, "size", curatePoolSize, "draws per pool")
	f.StringSliceVar(&keys, "keys", nil, "pool keys to curate (default: every registered PoolKey)")
	return cmd
}

// registeredPoolKeys collects every distinct PoolKey the registered workloads
// declare, sorted for determinism.
func registeredPoolKeys() []string {
	seen := map[string]bool{}
	for _, wl := range workload.All() {
		for _, q := range wl.Queries {
			if q.PoolKey != "" {
				seen[q.PoolKey] = true
			}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// curatePools writes one pool per requested key into params.json.
//
// A key that workload.Curate knows is curated properly: k-hop seeds drawn
// across out-degree bands, shortest-path pairs spread over BFS distance,
// edge probes half hits and half verified misses. Those properties are the
// difference between measuring a traversal and measuring whichever nodes a
// sampler happened to pick.
//
// Only an unknown key falls back to the shape heuristic below, which reads
// intent from the key's name: pair-shaped keys ("*-sp", "*edge*", "*pair*",
// "*path*") get {src,dst} bindings; miss-shaped keys ("*miss*") get
// identifiers guaranteed absent; everything else gets a single-node binding
// carried under both "id" and "seed" so either spelling resolves. A pool
// built that way is a placeholder, and the run says so.
func curatePools(ds engine.Dataset, keys []string, size int, seed int64) error {
	if ds.Dir() == "" {
		return fmt.Errorf("dataset %s has no directory (statements-only)", ds.Name())
	}
	if size <= 0 {
		size = curatePoolSize
	}

	pools := map[string]dataset.Pool{}
	var heuristic []string
	for _, key := range keys {
		pool, err := workload.Curate(ds, key, size, seed)
		if err == nil && len(pool) > 0 {
			pools[key] = dataset.Pool(pool)
			continue
		}
		heuristic = append(heuristic, key)
	}
	if len(heuristic) == 0 {
		return dataset.WriteParamsPool(filepath.Join(ds.Dir(), "params.json"), pools)
	}

	ids, err := sampleNodeIDs(ds, 4*size, seed)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return fmt.Errorf("dataset %s: no node ids found", ds.Name())
	}

	for _, key := range heuristic {
		pool := make(dataset.Pool, 0, size)
		lower := strings.ToLower(key)
		switch {
		case strings.Contains(lower, "miss"):
			// Misses are drawn past the largest present id rather than
			// spelled "__miss_N__": a token of the wrong type misses on
			// type, which is a different (and often cheaper) path than
			// the negative lookup this pool stands for.
			for i := 0; i < size; i++ {
				pool = append(pool, map[string]engine.Value{
					"id": missBeyond + int64(i), "seed": missBeyond + int64(i),
				})
			}
		case strings.HasSuffix(lower, "-sp") || strings.Contains(lower, "edge") ||
			strings.Contains(lower, "pair") || strings.Contains(lower, "path"):
			for i := 0; i < size; i++ {
				src := idToken(ids[(2*i)%len(ids)])
				dst := idToken(ids[(2*i+1)%len(ids)])
				pool = append(pool, map[string]engine.Value{"src": src, "dst": dst})
			}
		default:
			for i := 0; i < size; i++ {
				id := idToken(ids[i%len(ids)])
				pool = append(pool, map[string]engine.Value{"id": id, "seed": id})
			}
		}
		pools[key] = pool
	}
	return dataset.WriteParamsPool(filepath.Join(ds.Dir(), "params.json"), pools)
}

// sampleNodeIDs reservoir-samples up to n node identifiers across every node
// table of the dataset, deterministically from seed. The result is sorted so
// the pool order is stable regardless of file iteration order.
func sampleNodeIDs(ds engine.Dataset, n int, seed int64) ([]string, error) {
	rng := rand.New(rand.NewSource(seed))
	var reservoir []string
	seen := 0

	labels := make([]string, 0, len(ds.Schema().Nodes))
	for label := range ds.Schema().Nodes {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	for _, label := range labels {
		files, err := ds.NodeFiles(label)
		if err != nil {
			return nil, err
		}
		for _, path := range files {
			if err := sampleFile(path, n, rng, &reservoir, &seen); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		}
	}
	sort.Strings(reservoir)
	return reservoir, nil
}

// sampleFile streams one CSV file, reservoir-sampling the ID column.
func sampleFile(path string, n int, rng *rand.Rand, reservoir *[]string, seen *int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.ReuseRecord = true

	header, err := r.Read()
	if err != nil {
		return err
	}
	cols, err := dataset.ParseHeader(header)
	if err != nil {
		return err
	}
	idIdx := -1
	for i, c := range cols {
		if c.Type == "ID" {
			idIdx = i
			break
		}
	}
	if idIdx < 0 {
		return fmt.Errorf("no ID column in header")
	}

	for {
		rec, err := r.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if idIdx >= len(rec) {
			continue
		}
		id := rec[idIdx]
		*seen++
		if len(*reservoir) < n {
			*reservoir = append(*reservoir, id)
		} else if j := rng.Intn(*seen); j < n {
			(*reservoir)[j] = id
		}
	}
}

// missBeyond is the base id for placeholder miss pools: far above any id a
// generated dataset assigns, so the lookup misses on absence rather than on
// a type or format mismatch.
const missBeyond int64 = 1 << 40

// idToken types a sampled node id the way curation does, so a placeholder
// pool binds the same value shape as a curated one (see workload.Curate).
func idToken(id string) engine.Value {
	if v, err := strconv.ParseInt(id, 10, 64); err == nil {
		return v
	}
	return id
}
