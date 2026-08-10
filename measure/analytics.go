package measure

import (
	"context"
	"fmt"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// AnalyticsResult is the outcome of the analytics repetition protocol
// (spec 07 §1, 08 §4). Per-rep durations are kept, not just the summary:
// variance between repetitions is information for whole-graph kernels.
type AnalyticsResult struct {
	// PerQuery holds the kept per-repetition wall-clock durations for each
	// query id, in repetition order. When the first rep was discarded as
	// warmup it does not appear here.
	PerQuery map[string][]time.Duration

	// Stats summarizes the kept repetitions per query id (window 0, so no
	// throughput — analytics are single-stream wall-clock numbers).
	Stats map[string]Stat
}

// RunAnalytics executes the analytics protocol: single-stream — one query at
// a time, never concurrent (spec 07 §1) — with reps repetitions per op, each
// timed wall-clock with the result drained inside the measured region. When
// discardFirst is set the first repetition is executed but not recorded (the
// warm-mode warmup rep); cold-mode callers pass false. reps <= 0 defaults
// to 5, the spec default.
//
// An Exec error aborts the run and is returned wrapped with the query id and
// repetition: an analytical kernel that fails is news, not a tail sample.
func RunAnalytics(ctx context.Context, sess engine.Session, ops []engine.Op, reps int, discardFirst bool) (AnalyticsResult, error) {
	if reps <= 0 {
		reps = 5
	}
	out := AnalyticsResult{
		PerQuery: make(map[string][]time.Duration, len(ops)),
		Stats:    make(map[string]Stat, len(ops)),
	}
	for _, op := range ops {
		var samples []Sample
		for r := 0; r < reps; r++ {
			if err := ctx.Err(); err != nil {
				return out, fmt.Errorf("analytics %s rep %d: %w", op.QueryID, r+1, err)
			}
			start := tick()
			res, err := sess.Exec(ctx, op)
			if err != nil {
				return out, fmt.Errorf("analytics %s rep %d: %w", op.QueryID, r+1, err)
			}
			rows := drainAndClose(res)
			d := start.elapsed()
			if discardFirst && r == 0 {
				continue
			}
			samples = append(samples, Sample{Class: op.Class, QueryID: op.QueryID, Latency: d, Rows: rows})
			out.PerQuery[op.QueryID] = append(out.PerQuery[op.QueryID], d)
		}
		out.Stats[op.QueryID] = summarizeGroup(op.Class, samples, 0)
	}
	return out, nil
}
