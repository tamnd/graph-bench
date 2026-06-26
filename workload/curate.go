package workload

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/target"
)

// This file is the parameter curation step from doc 04 section 5. It reads a
// dataset's graph, computes the curated pools the micro workload needs, and
// writes them to params.json beside the manifest so every engine that runs
// against this dataset draws the same parameters. Curation is a one-time step
// per dataset per curation version; the harness reads params.json at run time
// and does not re-curate on every run.
//
// Five pool keys are written:
//
//   - "micro-khop": seed nodes sampled in degree bands (low, mid, high, hub),
//     so a k-hop expansion is measured at the easy case and the hard case and
//     not just at a lucky middle.
//   - "micro-sp": (src, dst) pairs sampled at chosen BFS distances (adjacent,
//     mid-diameter, full-diameter where the graph is finite), so a shortest-path
//     query is measured as a function of distance, not luck. The bidirectional
//     variant (micro-sp-bidir) draws from this same pool.
//   - "micro-point": a flat sample of existing ids for the index probe, whose
//     cost is degree-independent so no banding is needed.
//   - "micro-point-miss": ids guaranteed absent from the graph for the negative
//     lookup.
//   - "micro-triangle": no parameters (the triangle count is over the whole graph),
//     so the pool holds one empty set as a sentinel.

// SamplesPerBand is the number of seed nodes drawn from each degree band during
// curation. Four bands (low, mid, high, hub) at this factor each give a pool
// large enough for a PoolSource to cycle without hitting the same seed twice on
// short runs.
const SamplesPerBand = 4

// Curate computes the curated parameter pools for ds and writes them to
// params.json in ds.Dir(). The seed drives all deterministic sampling so the
// same dataset + seed always produces the same pools. It is safe to call
// multiple times; it merges into an existing params.json rather than replacing
// the whole file.
//
// Curate requires a file-backed dataset (ds.Dir() non-empty). A statements
// dataset has no canonical CSV to read from and is an error.
func Curate(ds target.Dataset, seed int64) error {
	if ds.Dir() == "" {
		return fmt.Errorf("curate: dataset %q has no directory (statements dataset?)", ds.Name())
	}
	g, err := LoadGraph(ds)
	if err != nil {
		return fmt.Errorf("curate: load graph %q: %w", ds.Name(), err)
	}

	pools := map[string][]target.Params{}

	khop, err := curateKHop(g, seed)
	if err != nil {
		return fmt.Errorf("curate: khop pool: %w", err)
	}
	pools["micro-khop"] = khop

	sp, err := curateSP(g, seed)
	if err != nil {
		return fmt.Errorf("curate: sp pool: %w", err)
	}
	pools["micro-sp"] = sp

	// The point lookup draws a flat sample of existing ids (its cost is index-only,
	// degree-independent, so a flat sample is fair) and its negative variant draws
	// ids guaranteed absent from the graph.
	pools["micro-point"] = curatePoint(g, seed)
	pools["micro-point-miss"] = curatePointMiss(g)

	// The triangle workload takes no parameters: the count is over the whole
	// graph, so the pool is one empty set, which makes the PoolSource cycle
	// exactly once per dataset.
	pools["micro-triangle"] = []target.Params{{}}

	path := filepath.Join(ds.Dir(), "params.json")
	return dataset.WriteParamsPool(path, pools)
}

// curateKHop bins the graph's nodes by out-degree into four bands and draws
// SamplesPerBand seed ids from each, in deterministic order. The id tokens
// (strings) are the form the workload queries use in their parameter maps
// ({"seed": "5"}).
func curateKHop(g *Graph, seed int64) ([]target.Params, error) {
	if g.NodeCount() == 0 {
		return nil, nil
	}

	// Build the degree sequence: (id token, out-degree) pairs.
	type entry struct {
		id  string
		deg int
	}
	entries := make([]entry, len(g.ids))
	for i, id := range g.ids {
		entries[i] = entry{id: id, deg: len(g.out[i])}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].deg != entries[j].deg {
			return entries[i].deg < entries[j].deg
		}
		return entries[i].id < entries[j].id
	})

	n := len(entries)
	// Divide into four bands. Bands may overlap on tiny graphs; that is fine
	// because the samples from overlapping bands will still produce a range.
	bands := [4][2]int{
		{0, n / 4},
		{n / 4, n / 2},
		{n / 2, 3 * n / 4},
		{3 * n / 4, n},
	}

	rng := rand.New(rand.NewSource(seed))
	var pool []target.Params
	for _, b := range bands {
		lo, hi := b[0], b[1]
		if lo >= hi {
			hi = lo + 1
			if hi > n {
				hi = n
			}
		}
		size := hi - lo
		k := SamplesPerBand
		if k > size {
			k = size
		}
		// Fisher-Yates partial shuffle of the band indices, taking the first k.
		idxs := make([]int, size)
		for i := range idxs {
			idxs[i] = lo + i
		}
		for i := 0; i < k; i++ {
			j := i + rng.Intn(size-i)
			idxs[i], idxs[j] = idxs[j], idxs[i]
		}
		for _, idx := range idxs[:k] {
			pool = append(pool, target.Params{"seed": entries[idx].id})
		}
	}
	return pool, nil
}

// curateSP draws (src, dst) pairs at a spread of BFS distances. It samples
// sources from the degree sequence (one per degree quartile), then BFS from
// each to find candidates at adjacent, mid-diameter, and full-diameter distances.
// On a graph where no pair reaches those distances, it uses whatever was found.
func curateSP(g *Graph, seed int64) ([]target.Params, error) {
	if g.NodeCount() < 2 {
		return nil, nil
	}

	n := len(g.ids)
	rng := rand.New(rand.NewSource(seed ^ 0xdeadbeef))

	// Gather a handful of source nodes across the degree range.
	sources := []int{0, n / 4, n / 2, 3 * n / 4, n - 1}

	var pool []target.Params
	seen := map[[2]string]struct{}{}
	for _, src := range sources {
		if src >= n {
			src = n - 1
		}
		dists := bfsFrom(g, src)
		// Sort reachable destinations by distance, then pick candidates at the
		// lower, middle, and upper end of the reachable distance range.
		type reach struct {
			dst  int
			dist int
		}
		var reachable []reach
		for dst, d := range dists {
			if d > 0 {
				reachable = append(reachable, reach{dst, d})
			}
		}
		if len(reachable) == 0 {
			continue
		}
		sort.Slice(reachable, func(i, j int) bool {
			if reachable[i].dist != reachable[j].dist {
				return reachable[i].dist < reachable[j].dist
			}
			return reachable[i].dst < reachable[j].dst
		})

		picks := []int{0, len(reachable) / 2, len(reachable) - 1}
		// Add one random pick from the middle half for variety.
		if len(reachable) > 4 {
			lo, hi := len(reachable)/4, 3*len(reachable)/4
			picks = append(picks, lo+rng.Intn(hi-lo))
		}
		for _, p := range picks {
			if p >= len(reachable) {
				p = len(reachable) - 1
			}
			r := reachable[p]
			srcID := g.ids[src]
			dstID := g.ids[r.dst]
			key := [2]string{srcID, dstID}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			pool = append(pool, target.Params{"src": srcID, "dst": dstID})
		}
	}
	return pool, nil
}

// PointSamples is the number of existing ids drawn for the point-lookup pool and
// the number of absent ids drawn for the negative variant. Sixteen is enough for
// a PoolSource to cycle without repeating on short runs while keeping params.json
// small.
const PointSamples = 16

// curatePoint draws a flat, deterministic spread of existing id tokens for the
// point lookup. The cost of a point lookup is index-only and does not depend on
// degree, so unlike the k-hop pool there is no degree banding: an even stride over
// the id list is a fair sample of the index.
func curatePoint(g *Graph, seed int64) []target.Params {
	n := len(g.ids)
	if n == 0 {
		return nil
	}
	k := PointSamples
	if k > n {
		k = n
	}
	// Shuffle a copy of the indices deterministically and take the first k, so the
	// sample is spread across the id space rather than clustered at the start.
	rng := rand.New(rand.NewSource(seed ^ 0x10c0))
	idxs := make([]int, n)
	for i := range idxs {
		idxs[i] = i
	}
	for i := 0; i < k; i++ {
		j := i + rng.Intn(n-i)
		idxs[i], idxs[j] = idxs[j], idxs[i]
	}
	pool := make([]target.Params, 0, k)
	for _, idx := range idxs[:k] {
		pool = append(pool, target.Params{"id": g.ids[idx]})
	}
	return pool
}

// curatePointMiss produces id tokens guaranteed absent from the graph, the
// parameters for the negative lookup. The synthetic generators emit a dense
// base-10 numeric id, so ids above the maximum present id are certain misses; the
// routine still checks HasNode so a non-dense id space cannot smuggle a present id
// into the miss pool.
func curatePointMiss(g *Graph) []target.Params {
	var maxID int64 = -1
	for _, id := range g.ids {
		if v, err := strconv.ParseInt(id, 10, 64); err == nil && v > maxID {
			maxID = v
		}
	}
	pool := make([]target.Params, 0, PointSamples)
	next := maxID + 1
	for len(pool) < PointSamples {
		tok := strconv.FormatInt(next, 10)
		next++
		if g.HasNode(tok) {
			continue
		}
		pool = append(pool, target.Params{"id": tok})
		if next > maxID+int64(PointSamples)*4+8 {
			break // safety bound; never reached on a dense id space
		}
	}
	return pool
}

// bfsFrom returns the shortest-path distances from src to all reachable nodes in
// the directed graph, using -1 for unreachable. It is the same BFS as
// Graph.ShortestPath but returns the full distance array for curateSP to inspect
// all reachable targets at once.
func bfsFrom(g *Graph, src int) []int {
	dist := make([]int, len(g.ids))
	for i := range dist {
		dist[i] = -1
	}
	dist[src] = 0
	queue := []int{src}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		for _, v := range g.out[u] {
			if dist[v] != -1 {
				continue
			}
			dist[v] = dist[u] + 1
			queue = append(queue, v)
		}
	}
	return dist
}
