package measure

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// fakeResult is an engine.Result that returns a fixed number of empty rows.
type fakeResult struct {
	n   int
	cur int
}

func (r *fakeResult) Columns() []string   { return []string{"x"} }
func (r *fakeResult) Next() bool          { r.cur++; return r.cur <= r.n }
func (r *fakeResult) Row() []engine.Value { return []engine.Value{nil} }
func (r *fakeResult) Err() error          { return nil }
func (r *fakeResult) Close() error        { return nil }

// fakeSession is a minimal in-package engine.Session for tests: it counts
// Exec calls and optionally adds latency, returns canned rows, or fails.
type fakeSession struct {
	calls   atomic.Int64
	latency time.Duration            // artificial sleep per Exec
	err     error                    // if non-nil, returned on every Exec
	rows    int                      // rows per result; 0 means the default 3
	perOp   map[string]time.Duration // optional per-QueryID latency override
}

func (s *fakeSession) Version(context.Context) (string, error) { return "fake-1.0", nil }

func (s *fakeSession) Load(context.Context, engine.Dataset) (engine.LoadStats, error) {
	return engine.LoadStats{}, nil
}

func (s *fakeSession) Exec(_ context.Context, op engine.Op) (engine.Result, error) {
	s.calls.Add(1)
	lat := s.latency
	if d, ok := s.perOp[op.QueryID]; ok {
		lat = d
	}
	if lat > 0 {
		time.Sleep(lat)
	}
	if s.err != nil {
		return nil, s.err
	}
	n := s.rows
	if n == 0 {
		n = 3
	}
	return &fakeResult{n: n}, nil
}

func (s *fakeSession) Begin(context.Context, engine.AccessMode) (engine.Tx, error) {
	return nil, errors.New("fake session: no transactions")
}

func (s *fakeSession) Close(context.Context) error { return nil }

// makeOps builds n schedule ops for the given class with empty text.
func makeOps(n int, class engine.Class) []Op {
	ops := make([]Op, n)
	for i := range ops {
		ops[i] = Op{Op: engine.Op{Class: class}}
	}
	return ops
}

// ms is a helper to make durations readable in test literals.
func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }
