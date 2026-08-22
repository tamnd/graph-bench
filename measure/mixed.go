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
// The slots are handed out to match the weights exactly and then shuffled
// with a PRNG seeded with seed, so the mix is the composition the workload
// asked for while the interleaving stays irregular the way real traffic is,
// and two builds with the same inputs and seed produce the identical
// schedule (the seed goes into the Condition stamp as MixSeed, spec 08 §7).
// A query's ops are consumed in order, cycling when its share exceeds the
// ops provided.
//
// Allocating rather than drawing is what makes a rare query measurable.
// SNB's mix gives its two delete shapes a tenth of a percent each, and with
// independent draws eight thousand queries left snb-id2 with no samples at
// all while snb-id3 got some: the report read n/a, which looks like a broken
// query and was a coin flip. It also means two runs measure the same mix
// instead of two samples of it, so a difference between them is the engine.
//
// A query whose share does not buy a whole slot still gets none, and a run
// that short genuinely did not measure it. The difference is that now that
// is a fact about the count and the weight rather than about the seed: a
// tenth of a percent needs a thousand slots, and gets exactly one of them
// every time.
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

	// Largest remainder: every query gets the whole slots its share buys,
	// then the slots left over go to the queries whose shares were cut by
	// the most.
	share := make([]int, len(ids))
	rem := make([]float64, len(ids))
	handed := 0
	for i, id := range ids {
		exact := weights[id] / totalWeight * float64(totalCount)
		share[i] = int(exact)
		rem[i] = exact - float64(share[i])
		handed += share[i]
	}
	order := make([]int, len(ids))
	for i := range order {
		order[i] = i
	}
	// Ties go to the id that sorts first, so the schedule does not depend
	// on how the sort happened to break them.
	sort.SliceStable(order, func(a, b int) bool { return rem[order[a]] > rem[order[b]] })
	for n := 0; handed < totalCount; n++ {
		share[order[n%len(order)]]++
		handed++
	}

	slots := make([]int, 0, totalCount)
	for i, count := range share {
		for range count {
			slots = append(slots, i)
		}
	}
	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(slots), func(a, b int) { slots[a], slots[b] = slots[b], slots[a] })

	cursor := make([]int, len(ids)) // per-query op cursor
	ops := make([]Op, 0, totalCount)
	for _, pick := range slots {
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
