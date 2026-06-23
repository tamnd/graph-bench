package workload

import "github.com/tamnd/graph-bench/target"

// ParamSource produces the bound parameters for one execution of a query. A
// query that takes no parameters carries a nil source; a query with a single
// canonical instance carries a fixed source; a query whose cost depends on which
// entity it starts from carries a source that draws from the dataset's curated
// pool.
//
// The draw is never uniform-random: on a power-law graph a uniform draw produces
// wildly varying cost dominated by whether it landed on a supernode, and a
// benchmarker who re-ran until a cheap draw appeared could publish a flattering
// number. The curated pool (built in the dataset package) holds parameter sets
// whose cost is stable, so a query measures the engine and not the luck of the
// seed. See doc 05 section 1.2.
type ParamSource interface {
	// Next returns the next parameter set. A fixed source always returns the
	// same set; a pool-backed source advances through the pool and wraps.
	Next() target.Params
	// Pool returns every parameter set this source will produce, in order, for
	// the validation pass, which runs each query once per parameter set.
	Pool() []target.Params
}

// Fixed is a ParamSource carrying one parameter set, used by queries that have a
// single canonical instance: a whole-graph triangle count, a property scan over
// the whole graph. A nil or empty set is valid (the query takes no parameters).
type Fixed struct {
	Set target.Params
}

// NewFixed returns a fixed source carrying one parameter set.
func NewFixed(set target.Params) *Fixed { return &Fixed{Set: set} }

func (f *Fixed) Next() target.Params   { return f.Set }
func (f *Fixed) Pool() []target.Params { return []target.Params{f.Set} }

// PoolSource is a ParamSource backed by a fixed, ordered list of parameter sets,
// cycling through them on each Next. It is the concrete source a curated pool
// produces: the dataset curates the list (degree-banded seeds, connected
// endpoint pairs, time windows of comparable selectivity) and hands it here, so
// the draw order is deterministic and a reproduction runs the same parameters as
// well as the same graph.
type PoolSource struct {
	sets []target.Params
	next int
}

// NewPool returns a source that cycles through sets in order. It copies the
// slice header so the caller's slice is not aliased for mutation, but it shares
// the parameter maps, which the validation pass treats as read-only.
func NewPool(sets []target.Params) *PoolSource {
	cp := make([]target.Params, len(sets))
	copy(cp, sets)
	return &PoolSource{sets: cp}
}

func (p *PoolSource) Next() target.Params {
	if len(p.sets) == 0 {
		return nil
	}
	set := p.sets[p.next%len(p.sets)]
	p.next++
	return set
}

func (p *PoolSource) Pool() []target.Params { return p.sets }
