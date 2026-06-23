package workload

import "github.com/tamnd/graph-bench/target"

// RefStrategy says how to compute the reference answer for a query on a dataset
// and how to compare an engine's answer to it. The strategy is not the answer;
// it is the recipe for the answer (computed once per dataset, engine-independent)
// and the rule for judging a match. See doc 05 sections 1.3 and 6.
type RefStrategy struct {
	// Compute produces the reference answer for one parameter set on a dataset.
	// It runs once per (query, dataset, parameter set) and the result is cached
	// in the dataset's reference file. It reads the canonical CSV through the
	// dataset (Dir, NodeFiles, RelFiles) or, for awkward answers, a trusted
	// engine run cross-checked against a second engine; it never reads the engine
	// under test, because the point is to catch an engine that is fast because it
	// is wrong. A nil Compute means the query carries no reference and is not
	// validated (which is itself a review failure for a shipped query).
	Compute func(ds target.Dataset, p target.Params) (*target.Answer, error)

	// Compare is the rule for judging an engine's answer against the reference:
	// the ordering rule, the float tolerance, the numeric-coercion flag, and the
	// element-id exclusion.
	Compare CompareSpec
}

// CompareSpec is the normalized comparison rule for one query. It is set by the
// query author from the query's semantics and applied by the validator (doc 05
// section 6.2). The zero value is the strict default: ordered comparison, the
// default float tolerance, no numeric coercion, element ids excluded.
type CompareSpec struct {
	// Ordered compares result rows in order when true; when false the validator
	// sorts both sides by a canonical key before comparing, so two engines that
	// emit the same set in different orders both validate. Set true only for a
	// query whose ORDER BY fully determines the order.
	Ordered bool

	// FloatTol is the relative tolerance for float comparison. Zero means the
	// default, 1e-9. Engines round differently, most visibly in averages.
	FloatTol float64

	// CoerceNum lets an int64 reference match a float64 engine value, which the
	// count and aggregate queries set because some engines return a count as a
	// float. The default is strict: an int is an int.
	CoerceNum bool

	// IncludeElementIDs compares node and relationship element ids when true.
	// The default (false) excludes them, because element ids are engine-specific:
	// two engines assign different ids to the same logical node. A query that
	// returns a stable property id (n.id) compares it as a property regardless of
	// this flag; this flag only governs the internal element id of a returned
	// graph object.
	IncludeElementIDs bool
}

// FloatTolerance returns the effective relative float tolerance, applying the
// 1e-9 default when the spec leaves it zero.
func (c CompareSpec) FloatTolerance() float64 {
	if c.FloatTol == 0 {
		return 1e-9
	}
	return c.FloatTol
}
