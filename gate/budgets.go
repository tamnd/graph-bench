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

// DurableWriteSyncs is how many durable syncs a write p99 may cost on a
// machine whose sync is slower than the table's write ceiling. One for
// the statement's own commit, one for a group commit already in flight
// that it has to wait out, and a factor of two because this gates a p99
// and the tail of a group is where the waiting lands.
//
// Both engines measured on this laptop sit at about three of them, 11.96
// ms and 12.01 ms p99 against a sync of about 3 ms, at eight concurrent
// writers. Two engines with nothing in common landing on the same number
// is the sign that the number is the drive's and not theirs.
const DurableWriteSyncs = 4

// WriteCeiling is the write budget for a plane on the machine the run was
// measured on. A durable commit costs one sync of that machine's storage
// and nothing an engine does goes below it, so on a disk where a sync is
// slower than the table says a write may be, the table is a statement
// about the disk and every correct engine fails it. The ceiling is then
// what the disk allows instead. syncNanos of -1, the unprobed run, or a
// disk fast enough for the table leaves the table's number alone.
//
// raised reports which of the two the caller got, so a pass under a
// ceiling the disk set reads as one.
func WriteCeiling(plane engine.Plane, syncNanos int64) (ceiling time.Duration, raised bool) {
	table, ok := BudgetFor(engine.Write, plane)
	if !ok {
		return 0, false
	}
	if syncNanos <= 0 {
		return table, false
	}
	floor := time.Duration(syncNanos) * DurableWriteSyncs
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
