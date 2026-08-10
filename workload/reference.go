package workload

import "github.com/tamnd/graph-bench/engine"

// Answer is an engine-independent expected result.
type Answer struct {
	Columns []string
	Rows    [][]engine.Value

	// Unordered permits any row order; rows are canonicalized before
	// comparison.
	Unordered bool
}

// RefStrategy states how a query's answer is computed and compared.
type RefStrategy struct {
	// Compute produces the expected answer from the canonical dataset
	// for one parameter binding. It is straight-line Go over the CSV
	// (ADR-9) — never another engine.
	Compute func(ds engine.Dataset, params Params) (*Answer, error)

	// Compare tunes the comparison.
	Compare CompareSpec

	// SampleSize caps validation draws for heavy oracles; 0 means the
	// runner default.
	SampleSize int
}

// CompareSpec tunes answer comparison.
type CompareSpec struct {
	// Ordered requires exact row order (default: unordered canonical).
	Ordered bool

	// FloatTol is the absolute tolerance for float comparison;
	// 0 means the default 1e-9.
	FloatTol float64

	// CoerceNum compares int64 and float64 as numbers.
	CoerceNum bool

	// IncludeElementIDs opts element ids into comparison (diagnostic
	// default is exclusion).
	IncludeElementIDs bool
}
