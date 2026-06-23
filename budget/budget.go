// Package budget encodes the graph-bench latency budget as code, so a run is
// checked against a constant in a Go file instead of a number copied into a
// comment. The budget is stated per query class, not as one flat number, because
// the classes scale differently: a point read is an index seek whose cost is
// fixed regardless of graph size, while an analytical scan's cost is the size of
// its answer and grows with the graph. So the p99 ceiling applies to the bounded
// classes (point read, traversal, subgraph match, write) that should not grow
// with the graph, and the analytical class carries no latency ceiling and is
// governed by flatness and regression instead.
package budget

import (
	"time"

	"github.com/tamnd/graph-bench/target"
)

// Class is target.Class, reused rather than redefined, so the budget, the
// workload, and the measurement all name a class the same way and there is
// no second enum to keep in sync.
type Class = target.Class

// The classes, re-exported from target for budget callers so a budget file
// reads without reaching across packages for the names.
const (
	PointRead  = target.PointRead
	Traversal  = target.Traversal
	Subgraph   = target.Subgraph
	Write      = target.Write
	Analytical = target.Analytical
)

// Ceiling is the latency ceiling for a class. P99 is the ninety-ninth-percentile
// a run must stay under; a zero P99 means the class has no time ceiling and is
// checked by other means (flatness, regression) rather than by an absolute
// latency.
type Ceiling struct {
	// P99 is the ninety-ninth-percentile ceiling the class must hold, or zero
	// when the class is not bounded by latency.
	P99 time.Duration
}

// Bounded reports whether the class is held to a latency ceiling at all. The
// gate skips an absolute latency assertion for an unbounded class rather than
// passing it vacuously.
func (c Ceiling) Bounded() bool { return c.P99 > 0 }

// Starting ceilings for the bounded classes, for gr in-process at SF1 on the
// controlled machine. These are starting points refined against the gr baseline,
// not received numbers: a point read is an index seek and should land in the tens
// of microseconds; a bounded traversal expands a fixed radius and should land in
// the low single-digit milliseconds; a subgraph match runs the LSQB pattern from
// a curated anchor and is given more room; a write is one mutation plus its index
// maintenance. The analytical class carries no ceiling and is checked by
// regression and scaling, not by an absolute latency. A larger scale or the Bolt
// plane uses its own budget set (BudgetSet) with looser ceilings.
const (
	pointReadP99 = 500 * time.Microsecond
	traversalP99 = 5 * time.Millisecond
	subgraphP99  = 25 * time.Millisecond
	writeP99     = 2 * time.Millisecond
)

// defaultCeilings is the budget table for gr in-process at SF1. The analytical
// class carries no ceiling because its cost is the size of its answer, which
// grows with the graph, so it is governed by regression and scaling.
var defaultCeilings = map[Class]Ceiling{
	PointRead:  {P99: pointReadP99},
	Traversal:  {P99: traversalP99},
	Subgraph:   {P99: subgraphP99},
	Write:      {P99: writeP99},
	Analytical: {}, // unbounded: checked by regression and scaling, not latency
}

// For returns the ceiling for a class from the default budget (gr in-process at
// SF1). An unknown class returns a zero ceiling, which Bounded reports as
// unbounded, so a class the budget does not know is never held to a latency it
// was not given.
func For(c Class) Ceiling { return defaultCeilings[c] }

// Set is a named collection of ceiling tables keyed by a label, so the smoke
// gate checks against the in-process SF1 table and a larger-scale run checks
// against its own looser ceilings. The set is what the gate Spec carries so a
// run names the budget it is held to and the verdict records which budget was
// used.
type Set map[string]map[Class]Ceiling

// Get returns the ceiling table for the named label, falling back to the
// default ceilings when the label is not found in the set.
func (s Set) Get(label string) map[Class]Ceiling {
	if s != nil {
		if t, ok := s[label]; ok {
			return t
		}
	}
	return defaultCeilings
}

// ForIn returns the ceiling for a class in the named budget set entry.
// It falls back to the default ceilings for an unknown label and returns
// a zero ceiling for an unknown class.
func ForIn(s Set, label string, c Class) Ceiling {
	table := s.Get(label)
	return table[c]
}

// DefaultSet is the budget set shipped with graph-bench. It has one entry,
// the in-process SF1 budget, keyed by "inproc/SF1". The gate uses this set
// by default; a user can supply a wider set with looser ceilings for larger
// scales or the Bolt plane.
var DefaultSet = Set{
	"inproc/SF1": defaultCeilings,
}
