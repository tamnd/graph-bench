// Package gate decides whether a benchmark run passes: absolute budgets,
// regression against a stored baseline, point-read flatness, and
// verification integrity (spec 08 §9). It gates exactly one engine — the
// comparative matrix reports competitors, it never fails them (F-contract).
package gate

import (
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// Budget is a p99 latency ceiling for one (class, plane) cell at the SF1
// dataset tier. The table is seeded from zu's own budget file so the
// harness and the engine agree on what "fast enough" means (spec 08 §6).
type Budget struct {
	Class engine.Class
	Plane engine.Plane
	P99   time.Duration
}

// budgets is the SF1 ceiling table from spec 08 §6. Analytical is absent
// deliberately: analytics are tracked, never budget-gated.
var budgets = []Budget{
	{engine.PointRead, engine.InProc, 500 * time.Microsecond},
	{engine.PointRead, engine.Subprocess, 2 * time.Millisecond},
	{engine.PointRead, engine.Bolt, 5 * time.Millisecond},

	{engine.Traversal, engine.InProc, 5 * time.Millisecond},
	{engine.Traversal, engine.Subprocess, 10 * time.Millisecond},
	{engine.Traversal, engine.Bolt, 20 * time.Millisecond},

	{engine.Subgraph, engine.InProc, 25 * time.Millisecond},
	{engine.Subgraph, engine.Subprocess, 50 * time.Millisecond},
	{engine.Subgraph, engine.Bolt, 100 * time.Millisecond},

	{engine.Aggregation, engine.InProc, 250 * time.Millisecond},
	{engine.Aggregation, engine.Subprocess, 500 * time.Millisecond},
	{engine.Aggregation, engine.Bolt, time.Second},

	{engine.Write, engine.InProc, 2 * time.Millisecond},
	{engine.Write, engine.Subprocess, 10 * time.Millisecond},
	{engine.Write, engine.Bolt, 20 * time.Millisecond},
}

// BudgetFor returns the p99 ceiling for a class on a plane, or false when
// the cell is unbudgeted (Analytical, Native plane, unknown class).
func BudgetFor(class engine.Class, plane engine.Plane) (time.Duration, bool) {
	for _, b := range budgets {
		if b.Class == class && b.Plane == plane {
			return b.P99, true
		}
	}
	return 0, false
}

// DurableWriteSyncs is how many durable syncs a write p99 may cost at a
// given client concurrency. Durable commits through one log are
// serialized, so a write that arrives when the log is busy waits: at
// worst behind the group already in flight, then behind each of the other
// clients that got there first, then its own. That is concurrency plus
// one, and group commit is what pulls the real number below it by letting
// several of those writers share a single flush.
//
// The shape is what the measurements show. At one client a write costs
// exactly one sync, 3.04 ms p50 against a 3.04 ms probe, so the write path
// above the disk costs nothing measurable. At eight clients on a mixed
// workload it is 6.16 ms p50, two syncs, because writers arrive too
// sparsely to fill a group and each one waits out the flush it landed
// behind. Pure writes at eight clients go the other way, 4 ms p50, one
// sync, because eight writers arriving at once are one group.
//
// Never below two: even alone, a write can arrive one instruction after a
// flush began.
func DurableWriteSyncs(concurrency int) int {
	if concurrency < 1 {
		return 2
	}
	return concurrency + 1
}

// WriteCeiling is the write budget for a plane on the machine and at the
// concurrency the run was measured at. A durable commit costs one sync of
// that machine's storage and nothing an engine does goes below it, so on a
// disk where a sync is slower than the table says a write may be, the
// table is a statement about the disk and every correct engine fails it.
// The ceiling is then what the disk allows instead. syncNanos of -1, the
// unprobed run, or a disk fast enough for the table leaves the table's
// number alone.
//
// raised reports which of the two the caller got, so a pass under a
// ceiling the disk set reads as one.
func WriteCeiling(plane engine.Plane, syncNanos int64, concurrency int) (ceiling time.Duration, raised bool) {
	table, ok := BudgetFor(engine.Write, plane)
	if !ok {
		return 0, false
	}
	if syncNanos <= 0 {
		return table, false
	}
	floor := time.Duration(syncNanos) * time.Duration(DurableWriteSyncs(concurrency))
	if floor <= table {
		return table, false
	}
	return floor, true
}

// Workload-specific gates layered on top of the class table (spec 08 §6).
const (
	// FinBenchReadP99 is the FinBench SLO: every fb-* read query holds
	// p99 < 100 ms on any plane.
	FinBenchReadP99 = 100 * time.Millisecond

	// SNBShortWarmP50 is zu goal G2: snb-short warm p50 < 1 ms on the
	// gated engine's in-process plane, when that plane exists.
	SNBShortWarmP50 = time.Millisecond

	// DefaultRegressionFactor is the allowed p50/p99 growth over the
	// stored baseline before the gate fails (--regression-factor).
	DefaultRegressionFactor = 1.10

	// DefaultDriftFactor is how much a sustained run's p99 may grow from
	// its first window to its worst before the run counts as degrading.
	// It is the regression factor turned on the run itself: a build that
	// may not be a tenth slower than yesterday's build may not be a tenth
	// slower than it was a minute ago either, and a store that fragments
	// or a backlog that builds shows up here while the run's single p99
	// averages the good opening in and hides it.
	DefaultDriftFactor = 1.10

	// DefaultFlatnessFactor bounds the small-vs-large dataset latency
	// ratio for PointRead: an index that works is flat.
	DefaultFlatnessFactor = 2.0

	// DefaultNoisePercentile is where the suggested noise floor is taken
	// from the per-query spreads: high enough that the floor covers most
	// of the suite, low enough that one pathological query does not set
	// the bar for every other.
	DefaultNoisePercentile = 90.0

	// Indeterminate is the Violation kind for a difference the gate will
	// not rule on, because the machine's own run-to-run spread is wider
	// than the difference. It never fails a run.
	Indeterminate = "indeterminate"
)

// Exit codes returned by the gate verb (spec 08 §9).
const (
	ExitPass         = 0
	ExitBudget       = 2 // budget or regression violation
	ExitVerification = 3 // verification failure, coverage regression, poisoned teardown
)
