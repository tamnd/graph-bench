package linkbench

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

const poolMax = 8

// BuildPools curates the read-operation parameter pools. LinkBench's
// access skew is hot-spot shaped (power-law over ids); the pools
// approximate it deterministically by drawing the hottest sources of
// the generator's power-law out-link distribution, ranked by out-degree
// with numeric-id tie-breaks, so every engine sees the identical
// pre-drawn sequence (spec 06 §5).
func BuildPools(ds engine.Dataset) (map[string][]workload.Params, error) {
	g, err := lbFor(ds)
	if err != nil {
		return nil, err
	}
	// One more than the pools need, so dropping the scratch object still
	// leaves a full pool when it turns out to be one of the hot ones.
	hot := withoutScratch(g.hotSources(poolMax + 1))
	if len(hot) > poolMax {
		hot = hot[:poolMax]
	}
	if len(hot) == 0 {
		return nil, fmt.Errorf("linkbench: dataset %s has no LINK sources", ds.Name())
	}

	nodePool := make([]workload.Params, 0, len(hot))
	for _, src := range hot {
		nodePool = append(nodePool, workload.Params{"id": workload.IDValue(src)})
	}

	var linkPool, listPool, countPool []workload.Params
	for _, src := range hot {
		lt := g.hotLtype(src)
		listPool = append(listPool, workload.Params{"src": workload.IDValue(src), "ltype": lt})
		countPool = append(countPool, workload.Params{"src": workload.IDValue(src), "ltype": lt})
		// The newest (ltype-matching) link of the hot source is the
		// get-link hit; determinism comes from the same total order the
		// list query fixes.
		rows := g.linksList(src, lt, 1)
		if len(rows) > 0 {
			linkPool = append(linkPool, workload.Params{
				"src": workload.IDValue(src), "ltype": lt, "dst": rows[0][0],
			})
		}
	}
	// One curated miss keeps the point read honest about absent
	// associations: ltype 96 is outside the generated 1..10 range.
	linkPool = append(linkPool, workload.Params{
		"src": workload.IDValue(hot[0]), "ltype": int64(96), "dst": workload.IDValue(g.ids[0]),
	})

	pools := map[string][]workload.Params{
		"lb-node":  nodePool,
		"lb-link":  linkPool,
		"lb-links": listPool,
		"lb-count": countPool,
	}
	for key, p := range pools {
		if len(p) == 0 {
			return nil, fmt.Errorf("linkbench: curated pool %q is empty on dataset %s", key, ds.Name())
		}
	}
	return pools, nil
}

// Bind installs the curated pools as the parameter sources of the
// workload's pooled queries.
func Bind(w *workload.Workload, ds engine.Dataset) error {
	pools, err := BuildPools(ds)
	if err != nil {
		return err
	}
	for _, q := range w.Queries {
		if q.PoolKey == "" {
			continue
		}
		p, ok := pools[q.PoolKey]
		if !ok {
			return fmt.Errorf("linkbench: no curated pool for key %q", q.PoolKey)
		}
		q.Params = workload.NewPoolSource(p)
	}
	return nil
}

// withoutScratch drops the object lb-update-node writes to. It has to
// go: lb-get-node checks an object's otype, version, time and payload
// against the values the generator wrote, and the update changes two of
// them, so a read pool holding it would report a verification failure
// for a write that worked. Nothing else in the mix reads an object's
// properties, so dropping it from every pool rather than only the node
// one costs nothing and keeps the four pools drawn from one list.
func withoutScratch(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != scratchID {
			out = append(out, id)
		}
	}
	return out
}

// hotSources returns up to k object ids ranked by out-link degree
// descending, numeric id ascending on ties.
func (g *lbGraph) hotSources(k int) []string {
	ids := make([]string, len(g.ids))
	copy(ids, g.ids)
	num := func(id string) int64 {
		n, _ := strconv.ParseInt(id, 10, 64)
		return n
	}
	sort.Slice(ids, func(i, j int) bool {
		di, dj := len(g.out[ids[i]]), len(g.out[ids[j]])
		if di != dj {
			return di > dj
		}
		return num(ids[i]) < num(ids[j])
	})
	if len(ids) > k {
		ids = ids[:k]
	}
	// Drop sources with no links at all (possible only on degenerate
	// configs; the generator emits at least one link per object).
	var out []string
	for _, id := range ids {
		if len(g.out[id]) > 0 {
			out = append(out, id)
		}
	}
	return out
}

// hotLtype returns the source's most frequent link type (smallest ltype
// on ties), so the (src, ltype) pools hit non-empty association lists.
func (g *lbGraph) hotLtype(src string) int64 {
	counts := map[int64]int{}
	for _, l := range g.out[src] {
		counts[l.ltype]++
	}
	var best int64
	bestN := -1
	lts := make([]int64, 0, len(counts))
	for lt := range counts {
		lts = append(lts, lt)
	}
	sort.Slice(lts, func(i, j int) bool { return lts[i] < lts[j] })
	for _, lt := range lts {
		if counts[lt] > bestN {
			best, bestN = lt, counts[lt]
		}
	}
	return best
}
