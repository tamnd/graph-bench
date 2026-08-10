package measure

import (
	"context"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// ColdRun executes each op exactly once, sequentially, and records the first-
// access latency into Result.Cold (spec 08 §4). It does not warm up, does not
// repeat, and does not use the open-model schedule; the caller is responsible
// for restarting the Session and dropping the OS page cache via the platform
// protocol before invoking ColdRun so the engine is actually cold.
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
// merges the two Results into the published Result with MergeCold; cold and
// warm are separate result sections, never merged into one distribution (F4).
func ColdRun(ctx context.Context, sess engine.Session, ops []Op, timeout time.Duration) Result {
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	samples := make([]Sample, 0, len(ops))
	for _, op := range ops {
		if ctx.Err() != nil {
			break
		}
		qctx, cancel := context.WithTimeout(ctx, timeout)
		s := Sample{Class: op.Op.Class, QueryID: op.Op.QueryID}
		intended := tick()
		res, err := sess.Exec(qctx, op.Op)
		if err == nil {
			s.Rows = drainAndClose(res)
		}
		s.Latency = intended.elapsed()
		cancel()
		s.Err = err
		samples = append(samples, s)
	}

	// Summarized by the same code as the warm run rather than accumulated by
	// hand. The hand-rolled version tracked only Max and called it P99 and
	// never assigned P50 at all, so every cold p50 in every report was the
	// zero value — rendered as "0.00ms", which reads as an instant cold
	// first access, the opposite of what a cold pass exists to show.
	//
	// A class here holds one sample per query rather than many samples of one
	// query, so its percentiles describe the spread of first-access cost
	// across that class's queries. That is the question a cold pass asks, and
	// with one query in a class the p50 and the p99 are that query's single
	// measurement — degenerate, but true, which the zero was not.
	byClass, _ := summarize(samples, 0)
	return Result{Cold: byClass}
}

// MergeCold merges a ColdRun result into a warm Result. The Cold map from
// the cold Result is set on the warm Result; if the warm Result already has
// a Cold map its entries are overwritten per class.
func MergeCold(warm, cold Result) Result {
	warm.Cold = cold.Cold
	return warm
}
