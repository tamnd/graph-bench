package workload

import (
	"math"
	"strconv"

	"github.com/tamnd/graph-bench/engine"
)

// Params is one parameter binding for a query execution.
type Params = map[string]engine.Value

// ParamSource yields deterministic parameter draws. Implementations must
// produce the identical sequence for every engine in a run (ADR-8).
type ParamSource interface {
	// Next returns the next draw, cycling deterministically.
	Next() Params
	// Pool returns the full pool for validation sampling; nil when the
	// source is fixed.
	Pool() []Params
}

// Fixed is a ParamSource that always returns the same binding.
type Fixed struct{ P Params }

func (f Fixed) Next() Params   { return f.P }
func (f Fixed) Pool() []Params { return nil }

// PoolSource cycles deterministically over a curated pool.
type PoolSource struct {
	pool []Params
	next int
}

// NewPoolSource wraps a curated pool.
func NewPoolSource(pool []Params) *PoolSource { return &PoolSource{pool: pool} }

func (p *PoolSource) Next() Params {
	if len(p.pool) == 0 {
		return nil
	}
	v := p.pool[p.next%len(p.pool)]
	p.next++
	return v
}

func (p *PoolSource) Pool() []Params { return p.pool }

// Token returns the parameter under key as the string token the oracle
// Graph and the canonical CSVs key on, accepting the integer and string
// spellings of the same id.
//
// Both spellings are legitimate. Curation types an integer id space as
// int64 so typed engines match their integer id property (see IDValue),
// while a dataset with non-numeric keys stays string; a reference that
// insisted on one spelling would reject half the datasets it is meant to
// check. Floats are accepted only when integral, since a fractional id
// is not an id.
func Token(p Params, key string) (string, bool) {
	v, ok := p[key]
	if !ok {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, true
	case int64:
		return strconv.FormatInt(t, 10), true
	case int:
		return strconv.Itoa(t), true
	case float64:
		if t == math.Trunc(t) {
			return strconv.FormatInt(int64(t), 10), true
		}
	}
	return "", false
}
