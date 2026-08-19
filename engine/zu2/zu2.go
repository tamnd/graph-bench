// Package zu2 is the graph-bench adapter for zu2, driven in-process over
// libzu2 (crates/zu2-capi): the database opens inside the harness process
// and every operation is a direct C call, with no frame, no pipe and no
// child process in the timed region. That is the plane ladybug and zu run
// on, so a zu2 column sits next to theirs and compares engines rather
// than transports.
//
// zu2 is not a database and this adapter does not pretend otherwise. It
// is a hash index over a hybrid log with an adjacency structure beside
// it, and it has no query language: no parser, no plan, no optimizer.
// What it has is an indexed record read, an adjacency load, and walks
// over that adjacency, and the primitive dialect (prim.go) is how a
// workload says which of those it wants. A workload query with no Prim
// text SKIPs here with "no-dialect-text", which is the honest outcome
// for a question this engine cannot be asked.
//
// So the comparison a zu2 row supports is a narrow one and worth being
// plain about: it is the cost of the storage and traversal underneath a
// query, against the cost of the same question asked of an engine that
// also has to parse and plan it. That is the whole point of the row.
// Where the rival is doing work zu2 is not, the gap is real but it is
// not all engine.
//
// The session itself is behind the zu2inproc build tag, because it needs
// libzu2 and its header on the machine. This file is the descriptor and
// it builds everywhere, so a binary built without the tag still lists
// zu2 and a run against it fails at Start with the build line to use
// rather than reporting an unknown engine.
package zu2

import (
	"github.com/tamnd/graph-bench/engine"
)

// Engine is the zu2 engine descriptor. Zero value is ready to use.
type Engine struct{}

// New returns the zu2 engine descriptor. Nothing opens until Start.
func New() *Engine { return &Engine{} }

var _ engine.Engine = (*Engine)(nil)

// Info reports zu2's static identity and its capabilities, which are
// what the C API has entry points for and nothing more.
func (e *Engine) Info() engine.Info {
	return engine.Info{
		Name:  "zu2",
		Plane: engine.InProc,
		// One dialect, and no Cypher behind it. There is nothing here
		// that could parse a Cypher text, so a chain with Cypher in it
		// would turn every unwritten query into a FAIL, and one FAIL
		// discards the measurement for every query in the workload.
		Dialects: []engine.Dialect{engine.Prim},
		Caps: engine.Capabilities{
			// No BEGIN and no COMMIT in the C API. A write is durable
			// when the session's durability mode says it is, which is a
			// property of the session and not of a transaction, so
			// Begin reports ErrNoTransactions rather than pretending.
			Transactions: false,
			// Load walks the dataset CSVs and calls add_vertex and
			// add_edge, which is the engine's own loading path and the
			// only one it has.
			BulkLoad: true,
			// remove_edge exists and no directive spells it yet.
			Deletes: false,
			// reach with a depth on it, which is the *1..k question.
			VarLengthPaths: true,
			ShortestPaths:  true,
			// The expansion takes a direction and a depth and nothing
			// else. There are no edge properties to gate on, so a
			// windowed traversal has no spelling here at all and saying
			// so makes those queries SKIP instead of quietly answering
			// the unwindowed question.
			PathPredicates: false,
			// No whole-graph kernels through this surface yet.
			Algorithms: nil,
			// A libzu2 session must not be in two calls at once and
			// answers ZU2_MISUSE_CONCURRENT rather than corrupting
			// anything when it is. The adapter keeps a pool of them, so
			// there is no bound for the adapter to declare and the
			// runner's own worker limit is the limit.
			MaxConcurrency: 0,
			Persistent:     true,
		},
	}
}

// result is the adapter's engine.Result. Every directive answers with
// one column and at most one row, so this is materialized before it is
// handed back and nothing streams.
type result struct {
	cols []string
	rows [][]engine.Value
	idx  int
}

var _ engine.Result = (*result)(nil)

// Columns reports the result column names, in order.
func (r *result) Columns() []string { return r.cols }

// Next advances to the next row and reports whether there was one.
func (r *result) Next() bool {
	if r.idx >= len(r.rows) {
		return false
	}
	r.idx++
	return true
}

// Row returns the current row, valid until the next Next.
func (r *result) Row() []engine.Value { return r.rows[r.idx-1] }

// Err reports the streaming error, of which there is none: the answer
// was in hand before the first Next.
func (r *result) Err() error { return nil }

// Close releases the result, which owns nothing the C side still holds.
func (r *result) Close() error { return nil }

// one builds a single-cell result under the directive's column name.
func one(column string, v engine.Value) *result {
	return &result{cols: []string{column}, rows: [][]engine.Value{{v}}}
}

// none builds an empty result under the directive's column name, which
// is the answer to a point read that missed and to a shortest path
// between two vertices with no path between them.
func none(column string) *result {
	return &result{cols: []string{column}}
}
