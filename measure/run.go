package measure

import (
	"context"
	"sync"
	"time"

	"github.com/tamnd/graph-bench/target"
)

// Op is one scheduled query: when it should arrive (Offset from run start),
// which budget class its latency counts against, and the resolved Query plus
// its bound parameters. Build-equivalent work (parameter selection, dialect
// resolution) is done when the schedule is built so firing an Op is a pure
// Driver.Run with no selection work on the hot path.
//
// QueryID is the workload query id (e.g. "snb-is2", "lsqb-q5") that produced
// this op. It propagates into Sample.QueryID so the result carries per-query
// statistics alongside the per-class rollup.
type Op struct {
	Offset  time.Duration
	Class   target.Class
	QueryID string
	Query   target.Query
	Params  target.Params
}

// Options tunes a run. The fields mirror benchload/harness.go's structure,
// generalized from HTTP to the Target SPI.
type Options struct {
	// Rate is the open-model offered rate in queries/second. The schedule
	// spaces arrivals at 1/Rate intervals so the offered load is exactly Rate
	// regardless of engine responsiveness.
	Rate float64

	// Count and Duration are mutually exclusive bounds on the run. Count fires
	// a fixed number of ops (micro-benchmarks and CI). Duration fires for a
	// fixed wall-clock time at Rate (throughput sweeps).
	Count    int
	Duration time.Duration

	// Warmup is the window at the start of the run where ops are fired but
	// not recorded. Steady-state measurement begins when an op's Offset
	// reaches or exceeds Warmup.
	Warmup time.Duration

	// Concurrency sizes the worker pool. 1 is the single-client isolated
	// latency measurement; larger values model a concurrent client population.
	// The pool must be large enough that the schedule, not the harness's own
	// resource limit, determines when queries are issued.
	Concurrency int

	// Timeout is the per-query context deadline. It defaults to 60 seconds
	// when zero. The timeout is generous on purpose: a slow engine should
	// record as slow, not escape the tail by timing out and being counted as
	// an error instead of a long latency (F10).
	Timeout time.Duration
}

// window returns the measured window duration: the total run duration minus
// the warmup window. It is used by summarize to compute Throughput.
func (o Options) window() time.Duration {
	if o.Duration > 0 {
		if o.Duration > o.Warmup {
			return o.Duration - o.Warmup
		}
		return 0
	}
	if o.Rate > 0 && o.Count > 0 {
		total := time.Duration(float64(o.Count) / o.Rate * float64(time.Second))
		if total > o.Warmup {
			return total - o.Warmup
		}
	}
	return 0
}

func (o Options) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return 60 * time.Second
}

// pool is a semaphore that bounds the number of goroutines concurrently inside
// the engine. It prevents the harness from serializing on its own resource
// limit, ensuring the open-model schedule (not the harness) decides when a
// query is issued.
type pool chan struct{}

func newPool(n int) pool {
	if n <= 0 {
		n = 1
	}
	ch := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		ch <- struct{}{}
	}
	return ch
}

func (p pool) acquire() { <-p }
func (p pool) release() { p <- struct{}{} }

// BuildSchedule sets the Offset field on each op to produce an evenly-spaced
// open-model schedule at the given rate. Ops at index 0..warmupCount-1 will
// have Offset < warmupCount/rate and be fired-but-not-recorded by Run.
// The caller builds the Op slice with Query and Params already resolved;
// BuildSchedule only assigns the arrival times.
func BuildSchedule(ops []Op, rate float64, warmup time.Duration) []Op {
	if rate <= 0 || len(ops) == 0 {
		return ops
	}
	interval := time.Duration(float64(time.Second) / rate)
	for i := range ops {
		ops[i].Offset = time.Duration(i) * interval
	}
	return ops
}

// Run fires every op against the driver and returns the per-class steady-state
// summary. The clock it measures against depends on the offered rate.
//
// With a rate (opt.Rate > 0) it is an open model: BuildSchedule has spaced the
// arrivals, each op is dispatched at its intended arrival time regardless of
// whether earlier ops have completed, and latency is measured from that intended
// arrival, so harness slippage and engine queueing both land in the number
// (coordinated-omission correction). This is the throughput-sweep number.
//
// Without a rate (opt.Rate <= 0, the count-mode default) there is no arrival
// schedule: every op has Offset 0, so they are all "due" at the start and the
// worker pool serializes them. Measuring from the shared start would then report
// each op's position in that queue, not the engine's speed, and the reported p50
// would scale with --count. So count mode measures from actual dispatch (after
// the pool admits the op) to completion: the per-query service time, with the
// queue excluded. Result.Latency records which clock was used.
//
// The context bounds the whole run; cancelling it stops dispatching new ops and
// lets in-flight ops settle.
func Run(ctx context.Context, d target.Driver, ops []Op, opt Options) Result {
	p := newPool(opt.Concurrency)
	samples := make([]Sample, len(ops))
	measured := make([]bool, len(ops))
	// serviceTime: with no offered rate the arrival schedule is meaningless and
	// timing from intended would measure queue depth, so time from dispatch.
	serviceTime := opt.Rate <= 0
	var wg sync.WaitGroup

	start := time.Now()
	for i, op := range ops {
		arrival := start.Add(op.Offset)
		if wait := time.Until(arrival); wait > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				goto drain
			}
		}

		if op.Offset < opt.Warmup {
			// Fire warmup ops so the engine is loaded as the schedule intends,
			// but do not record them in the steady-state window.
			wg.Add(1)
			go func(o Op) {
				defer wg.Done()
				p.acquire()
				defer p.release()
				qctx, cancel := context.WithTimeout(ctx, opt.timeout())
				defer cancel()
				if res, err := d.Run(qctx, o.Query, o.Params); err == nil {
					drainAndClose(res)
				}
			}(op)
			continue
		}

		measured[i] = true
		wg.Add(1)
		go func(idx int, o Op, intended time.Time) {
			defer wg.Done()
			p.acquire()
			defer p.release()
			s := Sample{Class: o.Class, QueryID: o.QueryID}
			// dispatch is the moment the pool admits this op. In count mode the
			// queue ahead of it is not the engine's latency, so we start the clock
			// here; in open-model mode we start from the intended arrival below.
			dispatch := time.Now()
			qctx, cancel := context.WithTimeout(ctx, opt.timeout())
			defer cancel()
			res, err := d.Run(qctx, o.Query, o.Params)
			if serviceTime {
				s.Latency = time.Since(dispatch)
			} else {
				s.Latency = time.Since(intended)
			}
			if err != nil {
				s.Err = err
				samples[idx] = s
				return
			}
			s.Rows = drainAndClose(res)
			samples[idx] = s
		}(i, op, arrival)
	}

drain:
	wg.Wait()
	steady := make([]Sample, 0, len(ops))
	for i := range samples {
		if measured[i] {
			steady = append(steady, samples[i])
		}
	}
	byClass, byQuery := summarize(steady, opt.window())
	model := OpenModelLatency
	if serviceTime {
		model = ServiceTimeLatency
	}
	return Result{Stats: byClass, ByQuery: byQuery, Latency: model}
}

// drainAndClose streams the result to count its rows and then releases it, so
// an adapter that pools a connection gets it back for reuse instead of leaking
// it. It returns the row count for the Sample.
func drainAndClose(res target.Result) int {
	rows := 0
	for res.Next() {
		rows++
	}
	_ = res.Close()
	return rows
}
