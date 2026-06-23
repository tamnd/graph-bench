package measure

import (
	"context"
	"time"

	"github.com/tamnd/graph-bench/target"
)

// ColdRun executes each op exactly once, sequentially, and records the first-
// access latency into Result.Cold. It does not warm up, does not repeat, and
// does not use the open-model schedule; the caller is responsible for having
// called setup.DropCaches before invoking ColdRun so the engine is cold.
//
// Sequential execution is intentional: running the ops in parallel would warm
// the engine for ops that fire slightly later, corrupting the first-access
// measurement for those ops. Each op in the slice should represent a distinct
// query so the cold map carries one latency per query class.
//
// The per-query timeout defaults to 60 seconds when zero, same generous rule
// as Run: a slow cold read is recorded as slow, not cut off as an error (F10).
//
// On return, Result.Cold is populated and Result.Stats is nil. The caller
// merges the two Results into the published Result; the gate (doc 07) checks
// both maps for its SLO assertions.
func ColdRun(ctx context.Context, d target.Driver, ops []Op, timeout time.Duration) Result {
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	cold := make(map[target.Class]Stat, 4)
	for _, op := range ops {
		if ctx.Err() != nil {
			break
		}
		qctx, cancel := context.WithTimeout(ctx, timeout)
		s := Sample{Class: op.Class}
		intended := time.Now()
		res, err := d.Run(qctx, op.Query, op.Params)
		s.Latency = time.Since(intended)
		cancel()
		if err != nil {
			s.Err = err
		} else {
			s.Rows = drainAndClose(res)
		}
		// Accumulate into a per-class Stat manually since we have one sample per op.
		stat := cold[op.Class]
		stat.Class = op.Class
		stat.Count++
		if s.Err != nil {
			stat.Errors++
		} else {
			// For cold runs, each sample is likely a distinct query with its own
			// latency. Record the latency and track max; the caller can derive the
			// distribution if they run multiple cold ops per class.
			if s.Latency > stat.Max {
				stat.Max = s.Latency
			}
			if stat.P99 == 0 || s.Latency > stat.P99 {
				stat.P99 = s.Latency
			}
		}
		cold[op.Class] = stat
	}
	return Result{Cold: cold}
}

// MergeCold merges a ColdRun result into a warm Result. The Cold map from
// the cold Result is set on the warm Result; if the warm Result already has
// a Cold map its entries are overwritten per class.
func MergeCold(warm, cold Result) Result {
	warm.Cold = cold.Cold
	return warm
}
