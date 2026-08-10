package workload

import (
	"fmt"
	"math/rand"
	"sort"
	"strconv"

	"github.com/tamnd/graph-bench/engine"
)

// This file is the parameter curation step (spec 05 §4): it reads a dataset's
// graph through the oracle loader and computes the curated pools the micro
// family draws from, deterministically from a seed, so every engine in a run
// sees the identical parameter sequence (ADR-8). Curation never touches an
// engine; it is straight-line Go over the canonical CSV, the same trust base
// as the reference oracles.
//
// The pool keys are carried from v1 so the registered families and the
// dataset layer agree on the names:
//
//   - "micro-khop": seed nodes sampled in out-degree bands (low, mid, high,
//     hub), so a k-hop expansion is measured at the easy case and the hard
//     case and not just at a lucky middle. Params: {"seed": id}.
//   - "micro-sp": (src, dst) pairs sampled at a spread of BFS distances
//     (adjacent, mid-range, far), so a shortest-path query is measured as a
//     function of distance, not luck. Params: {"src", "dst"}.
//   - "micro-point": a flat sample of existing ids for the index probe,
//     whose cost is degree-independent so no banding is needed.
//   - "micro-point-miss": ids guaranteed absent from the graph for the
//     negative lookup.
//   - "micro-edge": (src, dst) pairs for the edge-existence probe, half
//     existing directed edges and half absent pairs, so both the hit and the
//     miss path are measured.
//   - "micro-triangle": no parameters (the count is over the whole graph);
//     the pool is one empty binding as a sentinel.

// IDValue types a raw CSV node id for the value model: a base-10 integer id
// becomes int64, anything else stays a string.
//
// The type is not cosmetic, and it binds both directions of the comparison.
// Canonical datasets write integer ids, and a typed engine stores them as
// integers, so a string parameter makes `{id: $id}` compare "104" against 104
// and match nothing — every point read silently returns no rows. It is worse
// than a wrong number: on the miss pool a type mismatch is a miss for the
// wrong reason, and a lookup that fails on type can be cheaper than the real
// negative lookup it is standing in for. The same applies to a reference
// answer: an oracle reads ids out of the CSV as text, and a reference row
// holding "104" where the engine returns 104 fails a correct engine.
//
// Every id that crosses into the value model — parameter or reference —
// therefore passes through here, so the two sides agree by construction
// rather than by each author remembering.
func IDValue(id string) engine.Value {
	if v, err := strconv.ParseInt(id, 10, 64); err == nil {
		return v
	}
	return id
}

// Curate computes the curated parameter pool named by poolKey for ds,
// deterministically from seed. size bounds the pool length (the sentinel
// "micro-triangle" pool is always a single empty binding); a graph too small
// to fill the request yields a shorter pool rather than an error. Curate
// requires a file-backed dataset (ds.Dir() non-empty): a statements dataset
// has no canonical CSV to read.
func Curate(ds engine.Dataset, poolKey string, size int, seed int64) ([]Params, error) {
	if ds.Dir() == "" {
		return nil, fmt.Errorf("curate: dataset %q has no directory (statements dataset?)", ds.Name())
	}
	if size <= 0 {
		return nil, fmt.Errorf("curate: pool %q: size must be > 0, got %d", poolKey, size)
	}
	g, err := LoadGraph(ds)
	if err != nil {
		return nil, fmt.Errorf("curate: load graph %q: %w", ds.Name(), err)
	}
	switch poolKey {
	case "micro-khop":
		return curateKHop(g, size, seed), nil
	case "micro-sp":
		return curateSP(g, size, seed), nil
	case "micro-point":
		return curatePoint(g, size, seed), nil
	case "micro-point-miss":
		return curatePointMiss(g, size), nil
	case "micro-edge":
		return curateEdge(g, size, seed), nil
	case "micro-triangle":
		// The triangle count is over the whole graph: one empty binding, so a
		// PoolSource cycles exactly once per dataset.
		return []Params{{}}, nil
	default:
		return nil, fmt.Errorf("curate: unknown pool key %q", poolKey)
	}
}

// curateKHop bins the graph's nodes by out-degree into four bands (low, mid,
// high, hub) and draws an even share of size seed ids from each, in
// deterministic order. Ids bind under their data type, so an integer id
// space produces {"seed": int64(5)} (see IDValue).
func curateKHop(g *Graph, size int, seed int64) []Params {
	n := g.NodeCount()
	if n == 0 {
		return nil
	}

	// The degree sequence: (id token, out-degree), sorted by degree then id so
	// the band boundaries are deterministic.
	type entry struct {
		id  string
		deg int
	}
	entries := make([]entry, n)
	for i, id := range g.ids {
		entries[i] = entry{id: id, deg: len(g.out[i])}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].deg != entries[j].deg {
			return entries[i].deg < entries[j].deg
		}
		return entries[i].id < entries[j].id
	})

	// Four bands; on tiny graphs bands may overlap by a node, which still
	// produces a degree range.
	bands := [4][2]int{
		{0, n / 4},
		{n / 4, n / 2},
		{n / 2, 3 * n / 4},
		{3 * n / 4, n},
	}
	perBand := (size + len(bands) - 1) / len(bands)

	rng := rand.New(rand.NewSource(seed))
	var pool []Params
	for _, b := range bands {
		lo, hi := b[0], b[1]
		if lo >= hi {
			hi = lo + 1
			if hi > n {
				hi = n
			}
		}
		width := hi - lo
		k := perBand
		if k > width {
			k = width
		}
		// Fisher-Yates partial shuffle of the band indices, taking the first k.
		idxs := make([]int, width)
		for i := range idxs {
			idxs[i] = lo + i
		}
		for i := 0; i < k; i++ {
			j := i + rng.Intn(width-i)
			idxs[i], idxs[j] = idxs[j], idxs[i]
		}
		for _, idx := range idxs[:k] {
			pool = append(pool, Params{"seed": IDValue(entries[idx].id)})
		}
	}
	if len(pool) > size {
		pool = pool[:size]
	}
	return pool
}

// curateSP draws (src, dst) pairs at a spread of BFS distances. Sources are
// taken across the id space; from each, a BFS finds every reachable target,
// and picks land at the near, middle, and far end of the distance range plus
// random middle draws until the source's share of size is filled. A graph
// where nothing is reachable yields a short (possibly empty) pool.
func curateSP(g *Graph, size int, seed int64) []Params {
	n := g.NodeCount()
	if n < 2 {
		return nil
	}
	rng := rand.New(rand.NewSource(seed ^ 0xdeadbeef))
	sources := []int{0, n / 4, n / 2, 3 * n / 4, n - 1}
	perSource := (size + len(sources) - 1) / len(sources)

	var pool []Params
	seen := map[[2]string]struct{}{}
	add := func(src, dst int) {
		key := [2]string{g.ids[src], g.ids[dst]}
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		pool = append(pool, Params{"src": IDValue(g.ids[src]), "dst": IDValue(g.ids[dst])})
	}

	for _, src := range sources {
		if src >= n {
			src = n - 1
		}
		dists := curateBFS(g, src)
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

		before := len(pool)
		picks := []int{0, len(reachable) / 2, len(reachable) - 1}
		for _, p := range picks {
			if len(pool)-before >= perSource || len(pool) >= size {
				break
			}
			add(src, reachable[p].dst)
		}
		// Random middle-half picks until this source's share is filled; the
		// attempt bound keeps termination obvious on tiny reachable sets.
		lo, hi := len(reachable)/4, 3*len(reachable)/4
		if hi <= lo {
			lo, hi = 0, len(reachable)
		}
		for attempts := 0; len(pool)-before < perSource && len(pool) < size && attempts < 4*perSource; attempts++ {
			add(src, reachable[lo+rng.Intn(hi-lo)].dst)
		}
		if len(pool) >= size {
			break
		}
	}
	if len(pool) > size {
		pool = pool[:size]
	}
	return pool
}

// curatePoint draws a flat, deterministic spread of size existing id tokens
// for the point lookup. A point lookup is index-only and degree-independent,
// so unlike the k-hop pool there is no banding: a shuffled prefix over the id
// list is a fair sample of the index.
func curatePoint(g *Graph, size int, seed int64) []Params {
	n := g.NodeCount()
	if n == 0 {
		return nil
	}
	k := size
	if k > n {
		k = n
	}
	rng := rand.New(rand.NewSource(seed ^ 0x10c0))
	idxs := make([]int, n)
	for i := range idxs {
		idxs[i] = i
	}
	for i := 0; i < k; i++ {
		j := i + rng.Intn(n-i)
		idxs[i], idxs[j] = idxs[j], idxs[i]
	}
	pool := make([]Params, 0, k)
	for _, idx := range idxs[:k] {
		pool = append(pool, Params{"id": IDValue(g.ids[idx])})
	}
	return pool
}

// curatePointMiss produces size id tokens guaranteed absent from the graph,
// the parameters for the negative lookup. The synthetic generators emit a
// dense base-10 numeric id, so ids above the maximum present id are certain
// misses; HasNode is still checked so a non-dense id space cannot smuggle a
// present id into the miss pool.
func curatePointMiss(g *Graph, size int) []Params {
	var maxID int64 = -1
	for _, id := range g.ids {
		if v, err := strconv.ParseInt(id, 10, 64); err == nil && v > maxID {
			maxID = v
		}
	}
	pool := make([]Params, 0, size)
	next := maxID + 1
	bound := maxID + int64(size)*4 + 8 // safety bound; never reached on a dense id space
	for len(pool) < size && next <= bound {
		tok := strconv.FormatInt(next, 10)
		next++
		if g.HasNode(tok) {
			continue
		}
		pool = append(pool, Params{"id": IDValue(tok)})
	}
	return pool
}

// curateEdge draws size (src, dst) pairs for the edge-existence probe: the
// first half are existing directed edges sampled uniformly, the second half
// are node pairs verified absent from the adjacency, so both answers of the
// boolean question are exercised. On a graph with no edges (or a complete
// one) the corresponding half comes up short rather than erroring.
func curateEdge(g *Graph, size int, seed int64) []Params {
	n := g.NodeCount()
	if n == 0 {
		return nil
	}
	rng := rand.New(rand.NewSource(seed ^ 0xed6e))

	// Flat edge index over the adjacency: edge e is the e'th (src, dst) in
	// node-then-neighbor order.
	var edges int
	for _, nbrs := range g.out {
		edges += len(nbrs)
	}

	hits := (size + 1) / 2
	if hits > edges {
		hits = edges
	}
	var pool []Params
	if edges > 0 {
		seen := map[int]struct{}{}
		for len(pool) < hits {
			e := rng.Intn(edges)
			if _, dup := seen[e]; dup {
				continue
			}
			seen[e] = struct{}{}
			src, off := 0, e
			for off >= len(g.out[src]) {
				off -= len(g.out[src])
				src++
			}
			pool = append(pool, Params{"src": IDValue(g.ids[src]), "dst": IDValue(g.ids[g.out[src][off]])})
		}
	}

	// Absent pairs: random (a, b) with a != b and no a->b edge. The attempt
	// bound keeps termination obvious on near-complete graphs.
	misses := size - len(pool)
	for attempts := 0; misses > 0 && attempts < 64*size; attempts++ {
		a, b := rng.Intn(n), rng.Intn(n)
		if a == b || curateHasEdge(g, a, b) {
			continue
		}
		pool = append(pool, Params{"src": IDValue(g.ids[a]), "dst": IDValue(g.ids[b])})
		misses--
	}
	return pool
}

// curateHasEdge reports whether the directed edge a->b is present, by binary
// search over the sorted adjacency.
func curateHasEdge(g *Graph, a, b int) bool {
	nbrs := g.out[a]
	i := sort.SearchInts(nbrs, b)
	return i < len(nbrs) && nbrs[i] == b
}

// curateBFS returns the shortest-path distances from src to every node in the
// directed graph, -1 for unreachable: the full distance array curateSP
// inspects to place picks along the distance range.
func curateBFS(g *Graph, src int) []int {
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
