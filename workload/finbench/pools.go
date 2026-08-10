package finbench

import (
	"fmt"
	"sort"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

// Pool curation (spec 05 §4, 06 §4): every windowed query draws
// (startTime, endTime) windows aligned to the dataset's TRANSFER ts
// range, and every pool entry is verified against the reference so the
// drawn parameters produce non-empty (non-degenerate) answers wherever
// the data admits any. Curation is deterministic: candidates are ranked
// by degree with id tie-breaks and windows are fixed quarters of the
// simulation clock plus the full range, so every engine sees the
// identical pool.

const poolMax = 8

// BuildPools curates the parameter pools for every fb-read query,
// keyed by PoolKey (which equals the query ID).
func BuildPools(ds engine.Dataset) (map[string][]workload.Params, error) {
	g, err := finFor(ds)
	if err != nil {
		return nil, err
	}
	pools := map[string][]workload.Params{
		"fb-tcr1":  tcr1Pool(g),
		"fb-tcr2":  tcr2Pool(g),
		"fb-tcr3":  tcr3Pool(g),
		"fb-tcr4":  tcr4Pool(g),
		"fb-tcr5":  tcr5Pool(g),
		"fb-tcr8":  tcr8Pool(g),
		"fb-tcr11": tcr11Pool(g),
		"fb-tcr12": tcr12Pool(g),
		"fb-sr1":   sr1Pool(g),
		"fb-sr2":   sr2Pool(g),
		"fb-w1":    w1Pool(g),
	}
	for key, p := range pools {
		if len(p) == 0 {
			return nil, fmt.Errorf("finbench: curated pool %q is empty on dataset %s", key, ds.Name())
		}
	}
	return pools, nil
}

// Bind installs the curated pools as the parameter sources of a
// workload's pooled queries (the test and single-process harness path;
// the CLI may instead materialize the same pools into params.json).
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
			return fmt.Errorf("finbench: no curated pool for key %q", q.PoolKey)
		}
		q.Params = workload.NewPoolSource(p)
	}
	return nil
}

// windows returns the four quarter-range windows of the transfer clock
// plus the full range (last), the aligned windows every pool draws from.
func (g *finGraph) windows() [][2]int64 {
	span := g.maxTs - g.minTs + 1
	q := span / 4
	if q < 1 {
		q = 1
	}
	var ws [][2]int64
	for i := int64(0); i < 4; i++ {
		s := g.minTs + i*q
		e := s + q
		if i == 3 {
			e = g.maxTs + 1
		}
		ws = append(ws, [2]int64{s, e})
	}
	ws = append(ws, [2]int64{g.minTs, g.maxTs + 1})
	return ws
}

// topBy returns up to k account ids ranked by the given degree metric
// (descending), ties broken by id ascending.
func (g *finGraph) topBy(k int, degree func(id string) int) []string {
	ids := make([]string, 0, len(g.accounts))
	for _, a := range g.accounts {
		ids = append(ids, a.id)
	}
	sort.Slice(ids, func(i, j int) bool {
		di, dj := degree(ids[i]), degree(ids[j])
		if di != dj {
			return di > dj
		}
		return ids[i] < ids[j]
	})
	if len(ids) > k {
		ids = ids[:k]
	}
	return ids
}

func (g *finGraph) topOut(k int) []string {
	return g.topBy(k, func(id string) int { return len(g.out[id]) })
}

func (g *finGraph) topIn(k int) []string {
	return g.topBy(k, func(id string) int { return len(g.in[id]) })
}

func winParams(id string, w [2]int64) workload.Params {
	return workload.Params{"id": workload.IDValue(id), "startTime": w[0], "endTime": w[1]}
}

func tcr1Pool(g *finGraph) []workload.Params {
	var pool []workload.Params
	for _, id := range g.topOut(12) {
		for _, w := range g.windows() {
			if len(pool) >= poolMax {
				return pool
			}
			if len(g.khopOut(id, w[0], w[1], 2)) > 0 {
				pool = append(pool, winParams(id, w))
				break
			}
		}
	}
	return pool
}

func tcr2Pool(g *finGraph) []workload.Params {
	var pool []workload.Params
	for _, w := range g.windows() {
		if len(pool) >= 4 {
			break
		}
		var max int64
		for _, a := range g.accounts {
			srcs := map[string]bool{}
			for _, ed := range winEdges(g.in[a.id], w[0], w[1], 0) {
				srcs[ed.other] = true
			}
			if int64(len(srcs)) > max {
				max = int64(len(srcs))
			}
		}
		if max >= 1 {
			pool = append(pool, workload.Params{
				"startTime": w[0], "endTime": w[1], "threshold": max / 2,
			})
		}
	}
	return pool
}

func tcr3Pool(g *finGraph) []workload.Params {
	var pool []workload.Params
	for _, src := range g.topOut(12) {
		for _, w := range g.windows() {
			if len(pool) >= poolMax {
				return pool
			}
			dst, ok := g.farthest(src, w[0], w[1], 3)
			if !ok {
				continue
			}
			pool = append(pool, workload.Params{
				"src": workload.IDValue(src), "dst": workload.IDValue(dst), "startTime": w[0], "endTime": w[1],
			})
			break
		}
	}
	return pool
}

// farthest returns the node at the deepest BFS level <= maxHops from
// src in the window (smallest id at that level), preferring depth >= 2.
func (g *finGraph) farthest(src string, s, e int64, maxHops int) (string, bool) {
	visited := map[string]bool{src: true}
	frontier := []string{src}
	var best string
	var ok bool
	for hop := 1; hop <= maxHops; hop++ {
		next := map[string]bool{}
		for _, u := range frontier {
			for _, ed := range winEdges(g.out[u], s, e, hubCap) {
				if !visited[ed.other] {
					next[ed.other] = true
				}
			}
		}
		nf := make([]string, 0, len(next))
		for v := range next {
			visited[v] = true
			nf = append(nf, v)
		}
		sort.Strings(nf)
		if len(nf) > 0 {
			best, ok = nf[0], true
		}
		frontier = nf
		if len(frontier) == 0 {
			break
		}
	}
	return best, ok
}

func tcr4Pool(g *finGraph) []workload.Params {
	var pool []workload.Params
	for _, id := range g.topIn(16) {
		for _, w := range g.windows() {
			if len(pool) >= poolMax {
				return pool
			}
			if g.loops(id, w[0], w[1]) > 0 {
				pool = append(pool, winParams(id, w))
				break
			}
		}
	}
	if len(pool) == 0 {
		// No windowed money loop exists in the data; a zero answer is
		// still a valid, verifiable question.
		full := [2]int64{g.minTs, g.maxTs + 1}
		for _, id := range g.topIn(4) {
			pool = append(pool, winParams(id, full))
		}
	}
	return pool
}

func tcr5Pool(g *finGraph) []workload.Params {
	var pool []workload.Params
	for _, id := range g.topIn(12) {
		for _, w := range g.windows() {
			if len(pool) >= poolMax {
				return pool
			}
			inC, _, outC, _ := g.fanStats(id, w[0], w[1])
			if inC+outC > 0 {
				p := winParams(id, w)
				p["threshold"] = 1.0
				pool = append(pool, p)
				break
			}
		}
	}
	return pool
}

func tcr8Pool(g *finGraph) []workload.Params {
	var pool []workload.Params
	for _, loan := range g.loanIDs {
		if len(pool) >= poolMax {
			break
		}
		deps := g.deposits[loan]
		if len(deps) == 0 {
			continue
		}
		d := deps[0]
		for _, w := range [][2]int64{
			{d.ts, d.ts + 3*86400},
			{g.minTs, g.maxTs + 1},
		} {
			if g.loanChain(loan, w[0], w[1]) > 0 {
				pool = append(pool, winParams(loan, w))
				break
			}
		}
	}
	return pool
}

func tcr11Pool(g *finGraph) []workload.Params {
	var pool []workload.Params
	for _, id := range g.topOut(12) {
		for _, w := range g.windows() {
			if len(pool) >= poolMax {
				return pool
			}
			found := false
			for _, decay := range []float64{1.0, 2.0, 1000.0} {
				if g.decayReach(id, w[0], w[1], decay) > 0 {
					p := winParams(id, w)
					p["decay"] = decay
					pool = append(pool, p)
					found = true
					break
				}
			}
			if found {
				break
			}
		}
	}
	if len(pool) == 0 {
		p := winParams(g.topOut(1)[0], [2]int64{g.minTs, g.maxTs + 1})
		p["decay"] = 1000.0
		pool = append(pool, p)
	}
	return pool
}

func tcr12Pool(g *finGraph) []workload.Params {
	var pool []workload.Params
	for _, w := range g.windows() {
		if len(pool) >= 4 {
			break
		}
		var max int64
		for _, a := range g.accounts {
			if a.createTime < w[0] || a.createTime >= w[1] {
				continue
			}
			if n := int64(len(winEdges(g.out[a.id], w[0], w[1], 0))); n > max {
				max = n
			}
		}
		if max >= 1 {
			pool = append(pool, workload.Params{
				"startTime": w[0], "endTime": w[1], "threshold": max / 2,
			})
		}
	}
	return pool
}

// w1Pool draws the endpoint pairs fb-w1 writes between: consecutive accounts
// in file order, which exist at every scale (the generator requires at least
// two). The marker ts rides along in the binding because the post-condition
// matches on it, and it is the same constant on every draw so the teardown's
// literal finds whatever the write left behind.
func w1Pool(g *finGraph) []workload.Params {
	var pool []workload.Params
	for i := 0; i+1 < len(g.accounts) && len(pool) < poolMax; i += 2 {
		pool = append(pool, workload.Params{
			"src":    workload.IDValue(g.accounts[i].id),
			"dst":    workload.IDValue(g.accounts[i+1].id),
			"amount": 12.34,
			"ts":     w1MarkTs,
		})
	}
	return pool
}

func sr1Pool(g *finGraph) []workload.Params {
	seen := map[string]bool{}
	var pool []workload.Params
	for _, id := range append(g.topIn(4), g.topOut(4)...) {
		if seen[id] || len(pool) >= poolMax {
			continue
		}
		seen[id] = true
		pool = append(pool, workload.Params{"id": workload.IDValue(id)})
	}
	return pool
}

func sr2Pool(g *finGraph) []workload.Params {
	var pool []workload.Params
	for _, id := range g.topOut(12) {
		for _, w := range g.windows() {
			if len(pool) >= poolMax {
				return pool
			}
			if len(winEdges(g.out[id], w[0], w[1], 0)) > 0 {
				pool = append(pool, winParams(id, w))
				break
			}
		}
	}
	return pool
}
