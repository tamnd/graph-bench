package measure

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// Op is one scheduled operation: when it should arrive (Offset from run
// start) and the fully resolved engine.Op to execute. Build-equivalent work
// (parameter selection, dialect resolution) is done by the caller when the
// schedule is built, so firing an Op is a pure Session.Exec with no selection
// work on the hot path.
//
// Op.Op.QueryID propagates into Sample.QueryID so the result carries
// per-query statistics alongside the per-class rollup.
type Op struct {
	Offset time.Duration
	Op     engine.Op
}

// Bracket is the pair of untimed statements that surround one repetition of
// a write query: Setup stages whatever the write consumes, Teardown restores
// the pre-write state.
//
// Repetition is the reason it exists. A write is not a read run twice — it
// changes the graph it reads, so the second repetition of "add this person"
// meets a graph where that person already exists. Engines disagree about what
// happens next, and both answers are wrong to measure: an engine that enforces
// the primary key errors on every repetition after the first and reports the
// cost of a constraint violation, while an engine that does not enforce it
// silently accumulates duplicates and reports the cost of inserting into a
// table growing under the measurement. The mirror case is a write whose Setup
// staged its input: once the state is consumed, every later repetition matches
// nothing and reports the cost of an index probe that found no rows, which
// looks like a very fast write.
//
// Bracketing each repetition is what spec 06 means by a teardown that
// "restores the pre-write state so repetitions are stationary": repetition n
// and repetition 1 see the same graph, so the samples are draws from one
// distribution rather than a trajectory.
//
// The statements themselves are never timed, and repetitions of a bracketed
// query are serialized so one repetition's Teardown cannot delete the rows
// another's Setup just staged. Both are exact in count mode, where the clock
// starts after Setup. In an open-model run the clock starts at the intended
// arrival, so a bracketed write there carries its own staging and its wait for
// the serializing gate inside the reported latency — bracketed writes under an
// offered rate read high by that much, and the closed-model count-mode numbers
// are the ones to quote for per-write latency.
type Bracket struct{ Setup, Teardown string }

// Options tunes a run.
type Options struct {
	// Rate is the open-model offered rate in queries/second. The schedule
	// spaces arrivals at 1/Rate intervals so the offered load is exactly Rate
	// regardless of engine responsiveness (spec 08 §2).
	Rate float64

	// Count and Duration are mutually exclusive bounds on the run. Count fires
	// a fixed number of ops (micro-benchmarks and CI). Duration fires for a
	// fixed wall-clock time at Rate (throughput sweeps).
	Count    int
	Duration time.Duration

	// Warmup is the window at the start of the run where ops are fired but
	// not recorded. Steady-state measurement begins when an op's Offset
	// reaches or exceeds Warmup. Warmup samples are discarded (spec 08 §3).
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

	// Budget caps the wall-clock spent on each distinct QueryID in a
	// count-mode run: once a query has been dispatching for this long, its
	// remaining repetitions are dropped and it is summarized from the ones
	// that completed. Zero means run all of Count.
	//
	// It exists because Count is the wrong knob for a fast preview: the same
	// count costs a millisecond on a point read and minutes on a windowed
	// depth-4 path enumeration, so a profile that fixes the count cannot fix
	// the running time. Cutting the count instead would shrink the cheap
	// queries — which are the ones that need repetitions — while barely
	// denting the expensive one.
	//
	// It is per-query rather than per-run because a run holds every query in
	// the workload concatenated: a whole-run cap would spend itself on
	// whichever queries happen to come first and leave the last ones with no
	// measurement at all, which is a worse report than a coarse one.
	//
	// Truncation is safe for latency because count mode measures each
	// repetition from its own dispatch, so a percentile over 12 completed
	// repetitions means the same thing as over 100 — just with a coarser
	// tail. It is not safe for throughput, which is why only the fast
	// profile sets it and full-profile numbers are never budget-truncated.
	// Ignored when Rate > 0: an open-model run's arrival schedule is the
	// measurement, and stopping it early would change the offered load.
	Budget time.Duration

	// Brackets carries the Setup/Teardown pair for each write query, keyed by
	// QueryID. Queries absent from the map run unbracketed, which is every
	// read: a read leaves nothing behind, so there is nothing to restore.
	//
	// It is keyed by query rather than carried on Op because the statements
	// are a property of the query, identical across all of its repetitions,
	// and because every schedule builder — plain, mixed, cold — then gets the
	// behavior from the one Options it already passes.
	Brackets map[string]Bracket
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
// open-model schedule at the given rate. Ops whose Offset lands below warmup
// will be fired-but-not-recorded by Run. The caller builds the Op slice with
// the engine.Op fully resolved; BuildSchedule only assigns arrival times.
// A non-positive rate leaves offsets untouched (count mode has no schedule).
// warmup is carried in Options and is listed here only so call sites read as
// one line of schedule intent, matching v1.
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

// Run fires every op against the session and returns the per-class
// steady-state summary. The clock it measures against depends on the offered
// rate — the two clocks of spec 08 §2, never mixed in one result.
//
// With a rate (opt.Rate > 0) it is an open model: BuildSchedule has spaced the
// arrivals, each op is dispatched at its intended arrival time regardless of
// whether earlier ops have completed, and latency is measured from that
// intended arrival, so harness slippage and engine queueing both land in the
// number (coordinated-omission correction). This is the headline OLTP number.
//
// Without a rate (opt.Rate <= 0, the count-mode default) there is no arrival
// schedule: every op has Offset 0, so they are all "due" at the start and the
// worker pool serializes them. Measuring from the shared start would then
// report each op's position in that queue, not the engine's speed, and the
// reported p50 would scale with --count (the v1 bug, preserved as regression
// test TestCountModeLatencyIndependentOfCount). So count mode measures from
// actual dispatch (after the pool admits the op) to completion: the per-query
// service time, with the queue excluded. Result.Latency records which clock
// was used.
//
// The context bounds the whole run; cancelling it stops dispatching new ops
// and lets in-flight ops settle.
func Run(ctx context.Context, sess engine.Session, ops []Op, opt Options) Result {
	p := newPool(opt.Concurrency)
	samples := make([]Sample, len(ops))
	measured := make([]bool, len(ops))
	// serviceTime: with no offered rate the arrival schedule is meaningless and
	// timing from intended would measure queue depth, so time from dispatch.
	serviceTime := opt.Rate <= 0
	// began records when each QueryID first dispatched, so Budget can be read
	// per query rather than per run.
	began := map[string]time.Time{}
	// gate serializes the repetitions of each bracketed query. Two repetitions
	// running at once would interleave one's Setup with the other's Teardown
	// over the same scratch rows, which is the non-stationarity the bracket is
	// there to remove. Only bracketed queries take a gate, so concurrency in a
	// mixed run still applies across queries and to every read.
	gate := map[string]*sync.Mutex{}
	for id := range opt.Brackets {
		gate[id] = &sync.Mutex{}
	}
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

		// Count mode gates dispatch on a free worker slot rather than letting
		// the loop queue every op at once. The slot is what makes elapsed time
		// mean "work completed" instead of "goroutines spawned", which is what
		// Budget has to be able to read. Open-model runs keep the old path:
		// their arrivals are the measurement and must not wait on the engine.
		if serviceTime {
			if opt.Budget > 0 {
				id := op.Op.QueryID
				if t, seen := began[id]; !seen {
					began[id] = time.Now()
				} else if time.Since(t) >= opt.Budget {
					continue
				}
			}
			p.acquire()
		}

		if op.Offset < opt.Warmup {
			// Fire warmup ops so the engine is loaded as the schedule intends,
			// but do not record them in the steady-state window.
			wg.Add(1)
			go func(o Op) {
				defer wg.Done()
				if !serviceTime {
					p.acquire()
				}
				defer p.release()
				qctx, cancel := context.WithTimeout(ctx, opt.timeout())
				defer cancel()
				if b, ok := opt.Brackets[o.Op.QueryID]; ok {
					g := gate[o.Op.QueryID]
					g.Lock()
					defer g.Unlock()
					if err := untimed(qctx, sess, o.Op, b.Setup); err != nil {
						return
					}
					defer func() { _ = untimed(qctx, sess, o.Op, b.Teardown) }()
				}
				if res, err := sess.Exec(qctx, o.Op); err == nil {
					drainAndClose(res)
				}
			}(op)
			continue
		}

		measured[i] = true
		wg.Add(1)
		go func(idx int, o Op, intended time.Time) {
			defer wg.Done()
			if !serviceTime {
				p.acquire()
			}
			defer p.release()
			s := Sample{Class: o.Op.Class, QueryID: o.Op.QueryID}
			qctx, cancel := context.WithTimeout(ctx, opt.timeout())
			defer cancel()

			// The bracket runs outside the clock: staging the rows a write
			// consumes and deleting them afterwards is the harness's cost of
			// making the repetition stationary, not the engine's cost of doing
			// the write. A Setup that fails means the repetition never saw the
			// state it was supposed to act on, so the sample is recorded as an
			// error rather than as a fast write over an empty match.
			if b, ok := opt.Brackets[o.Op.QueryID]; ok {
				g := gate[o.Op.QueryID]
				g.Lock()
				defer g.Unlock()
				if err := untimed(qctx, sess, o.Op, b.Setup); err != nil {
					s.Err = fmt.Errorf("setup: %w", err)
					samples[idx] = s
					return
				}
				defer func() { _ = untimed(qctx, sess, o.Op, b.Teardown) }()
			}

			// dispatch is the moment the pool admits this op. In count mode the
			// queue ahead of it is not the engine's latency, so we start the clock
			// here; in open-model mode we start from the intended arrival below.
			dispatch := tick()
			res, err := sess.Exec(qctx, o.Op)
			if err == nil {
				// Drain before the clock stops so serialization cost is inside
				// the measured region for every engine equally (engine.Result).
				s.Rows = drainAndClose(res)
			}
			if serviceTime {
				s.Latency = dispatch.elapsed()
			} else {
				s.Latency = time.Since(intended)
			}
			s.Err = err
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

// untimed runs one bracket statement and discards its rows. It borrows the
// timed op's QueryID, Class and Dialect so an adapter that routes on them —
// picking a write connection, choosing a transaction mode — treats the
// staging statement the same way it treats the write it stages.
//
// An empty text is not an error: most write queries name only one half of the
// bracket, because an insert needs nothing staged and a delete leaves nothing
// behind.
func untimed(ctx context.Context, sess engine.Session, like engine.Op, text string) error {
	if text == "" {
		return nil
	}
	res, err := sess.Exec(ctx, engine.Op{
		QueryID: like.QueryID,
		Class:   engine.Write,
		Dialect: like.Dialect,
		Text:    text,
	})
	if err != nil {
		return err
	}
	for res.Next() {
	}
	if err := res.Err(); err != nil {
		_ = res.Close()
		return err
	}
	return res.Close()
}

// Fire runs one op inside its bracket and discards every outcome. It is the
// untimed sibling of a measured repetition, for the warmup pass: warmup fires
// a write as often as the measured run does, so it has to leave the graph in
// the same state, or warmup itself becomes the reason the measured
// repetitions meet a graph they were not written for. A zero Bracket makes it
// a plain fire-and-drain.
func Fire(ctx context.Context, sess engine.Session, op engine.Op, b Bracket) {
	if err := untimed(ctx, sess, op, b.Setup); err != nil {
		return
	}
	defer func() { _ = untimed(ctx, sess, op, b.Teardown) }()
	if res, err := sess.Exec(ctx, op); err == nil {
		drainAndClose(res)
	}
}

// drainAndClose streams the result to count its rows and then releases it, so
// an adapter that pools a connection gets it back for reuse instead of leaking
// it. It returns the row count for the Sample.
func drainAndClose(res engine.Result) int {
	rows := 0
	for res.Next() {
		rows++
	}
	_ = res.Close()
	return rows
}
