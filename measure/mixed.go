package measure

import (
	"math"
	"time"

	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
)

// BuildMixedSchedule builds a weighted, interleaved Op slice for a mixed
// workload run. It draws from wl.Queries using the firing weights in wl.Mix,
// resolving each query's text for the given dialect. Queries with no Mix entry
// are skipped (they do not appear in the mixed schedule).
//
// totalCount is the total number of ops in the schedule (before warmup). The
// count for each query is proportional to its Mix weight (rounded to the
// nearest integer, with a minimum of 1 for any query with a non-zero weight).
// The ops are interleaved in round-robin order by query so each query is
// evenly distributed across the schedule window rather than clustered.
//
// rate and warmup are forwarded to BuildSchedule after interleaving.
// The returned slice has Offset already set.
func BuildMixedSchedule(wl *workload.Workload, dialect workload.Dialect, totalCount int, rate float64, warmup time.Duration) []Op {
	if len(wl.Mix) == 0 || totalCount <= 0 {
		return nil
	}

	// Normalize weights to per-query counts.
	var totalWeight float64
	for _, w := range wl.Mix {
		totalWeight += w
	}
	if totalWeight <= 0 {
		return nil
	}

	type slot struct {
		q     *workload.WorkloadQuery
		count int
	}
	var slots []slot
	remaining := totalCount
	for _, q := range wl.Queries {
		w, ok := wl.Mix[q.ID]
		if !ok || w <= 0 {
			continue
		}
		frac := w / totalWeight
		cnt := int(math.Round(frac * float64(totalCount)))
		if cnt < 1 {
			cnt = 1
		}
		slots = append(slots, slot{q: q, count: cnt})
		remaining -= cnt
	}
	// Distribute rounding remainder to the highest-weight query.
	if remaining != 0 && len(slots) > 0 {
		slots[0].count += remaining
		if slots[0].count < 0 {
			slots[0].count = 0
		}
	}

	// Interleave: round-robin across slots. This spreads each query type
	// evenly through time instead of clustering all of one type together.
	var ops []Op
	maxCount := 0
	for _, sl := range slots {
		if sl.count > maxCount {
			maxCount = sl.count
		}
	}
	idx := make([]int, len(slots)) // per-slot param cursor
	for round := 0; round < maxCount; round++ {
		for si, sl := range slots {
			if round >= sl.count {
				continue
			}
			var params target.Params
			if sl.q.Params != nil {
				params = sl.q.Params.Next()
			}
			q, p, ok := sl.q.ResolveRun(dialect, nil)
			if !ok {
				// Query has no text for this dialect; skip it.
				continue
			}
			if p == nil {
				p = params
			}
			_ = idx[si] // silence unused warning
			ops = append(ops, Op{
				Class:   sl.q.Class,
				QueryID: sl.q.ID,
				Query:   q,
				Params:  p,
			})
		}
	}
	return BuildSchedule(ops, rate, warmup)
}

// BuildIsolatedOps builds an Op slice for an isolated run of a single
// WorkloadQuery. It draws count parameter sets from the query's pool in
// order. Ops are returned without Offset set; the caller calls BuildSchedule.
func BuildIsolatedOps(q *workload.WorkloadQuery, dialect workload.Dialect, count int) []Op {
	if count <= 0 {
		return nil
	}
	query, _, ok := q.ResolveRun(dialect, nil)
	if !ok {
		return nil
	}
	ops := make([]Op, 0, count)
	for i := 0; i < count; i++ {
		var params target.Params
		if q.Params != nil {
			params = q.Params.Next()
		}
		ops = append(ops, Op{
			Class:   q.Class,
			QueryID: q.ID,
			Query:   query,
			Params:  params,
		})
	}
	return ops
}

// MixedResult pairs the Result from a mixed run with the Workload whose Mix
// was used so the caller can annotate the condition stamp and report.
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
