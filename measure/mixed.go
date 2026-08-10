package measure

import (
	"math/rand"
	"sort"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// BuildMixedSchedule builds a deterministic weighted interleave for a mixed
// workload run. perQuery maps a query id to its ready-made ops for that query
// (parameters already drawn, dialect already resolved by the caller — dialect
// resolution is not this package's job in v0.3.0). weights gives each query
// id's firing weight; ids with a non-positive weight or no ops are skipped.
//
// Each of the totalCount slots is drawn from the weighted distribution using
// a PRNG seeded with seed, so the mix converges to the weights while the
// interleaving stays irregular the way real traffic is — and two builds with
// the same inputs and seed produce the identical schedule (the seed goes into
// the Condition stamp as MixSeed, spec 08 §7). A query's ops are consumed in
// order, cycling when the draw count exceeds the ops provided.
//
// rate and warmup are forwarded to BuildSchedule after interleaving; the
// returned slice has Offset already set.
func BuildMixedSchedule(perQuery map[string][]engine.Op, weights map[string]float64, seed int64, totalCount int, rate float64, warmup time.Duration) []Op {
	if totalCount <= 0 || len(weights) == 0 {
		return nil
	}

	// Deterministic id order: map iteration order must not shape the schedule.
	ids := make([]string, 0, len(weights))
	for id, w := range weights {
		if w > 0 && len(perQuery[id]) > 0 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return nil
	}
	var totalWeight float64
	for _, id := range ids {
		totalWeight += weights[id]
	}

	rng := rand.New(rand.NewSource(seed))
	cursor := make([]int, len(ids)) // per-query op cursor
	ops := make([]Op, 0, totalCount)
	for n := 0; n < totalCount; n++ {
		r := rng.Float64() * totalWeight
		pick := len(ids) - 1
		for i, id := range ids {
			r -= weights[id]
			if r < 0 {
				pick = i
				break
			}
		}
		pool := perQuery[ids[pick]]
		ops = append(ops, Op{Op: pool[cursor[pick]%len(pool)]})
		cursor[pick]++
	}
	return BuildSchedule(ops, rate, warmup)
}

// MixedResult pairs the Result from a mixed run with the per-query isolation
// pass so the report can render write-interference factors.
type MixedResult struct {
	Result
	// IsolatedByQuery holds per-query Results from the isolation pass that was
	// run before the mixed run. Nil when the caller skips isolation.
	IsolatedByQuery map[string]Result
}

// Interference returns the latency slowdown factor for the given query under
// the mix relative to its isolated latency. A factor > 1.0 means the query
// ran slower in the mix (write interference). Returns 0 if either the
// isolated or the mixed per-query stat is missing.
func (r MixedResult) Interference(queryID string) float64 {
	isolated, ok := r.IsolatedByQuery[queryID]
	if !ok {
		return 0
	}
	isoStat, ok := isolated.ByQuery[queryID]
	if !ok || isoStat.P99 == 0 {
		return 0
	}
	mixStat, ok := r.ByQuery[queryID]
	if !ok || mixStat.P99 == 0 {
		return 0
	}
	return float64(mixStat.P99) / float64(isoStat.P99)
}
