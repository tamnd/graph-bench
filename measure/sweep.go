package measure

import (
	"context"
	"sort"

	"github.com/tamnd/graph-bench/target"
)

// DefaultSweepPoints is the standard concurrency sweep on the controlled
// machine: powers of two from the isolated single-client point up through the
// contention an application generates without requiring larger hardware.
var DefaultSweepPoints = []int{1, 2, 4, 8, 16, 32}

// CISweepPoints is the truncated sweep for the 2-vCPU CI runner, where the
// full sweep would exceed the time budget. Three points capture the isolated
// latency, a modest concurrent load, and the saturation region.
var CISweepPoints = []int{1, 4, 16}

// IsolatedLatency runs the workload at Concurrency=1 and returns the single-
// client latency result. It is the cleanest possible latency measurement: one
// query in flight at a time, no queueing, no contention. The open model still
// applies (arrivals on schedule, latency from intended arrival), but at
// concurrency 1 with a modest rate the schedule and the engine rarely fight.
func IsolatedLatency(ctx context.Context, d target.Driver, ops []Op, opt Options) Result {
	o := opt
	o.Concurrency = 1
	return Run(ctx, d, ops, o)
}

// Sweep runs the workload at each concurrency point in points and returns a
// Result with Stats set to the single-client (points[0], typically 1)
// measurement and Sweep populated from all points. If ctx is cancelled during
// a point, that point's Run drains in-flight goroutines before returning and
// the sweep stops early with the points collected so far.
//
// The caller is expected to pass points in ascending order. Sweep does not
// sort them, so the Sweep field reflects the order the caller requested, which
// is the order in the lineage and the report.
func Sweep(ctx context.Context, d target.Driver, ops []Op, opt Options, points []int) Result {
	var sweepPts []SweepPoint
	var baseStats map[target.Class]Stat

	for i, c := range points {
		if ctx.Err() != nil {
			break
		}
		o := opt
		o.Concurrency = c
		r := Run(ctx, d, ops, o)
		if i == 0 {
			baseStats = r.Stats
		}
		// Collect one SweepPoint per class at this concurrency level.
		classes := make([]target.Class, 0, len(r.Stats))
		for cl := range r.Stats {
			classes = append(classes, cl)
		}
		sort.Slice(classes, func(a, b int) bool { return classes[a] < classes[b] })
		for _, cl := range classes {
			stat := r.Stats[cl]
			sweepPts = append(sweepPts, SweepPoint{
				Concurrency: c,
				Class:       cl,
				Throughput:  stat.Throughput,
				P99:         stat.P99,
			})
		}
	}
	return Result{
		Stats: baseStats,
		Sweep: sweepPts,
	}
}
