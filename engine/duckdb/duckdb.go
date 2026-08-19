// Package duckdb is the graph-bench adapter for DuckDB, in-process over
// the C library through marcboeker/go-duckdb. It sits in the comparison
// as the analytical relational engine: same plane and same process as
// SQLite and zu2, but a columnar store with a vectorized executor, which
// is a different answer to the same question and shows up as a different
// shape of number rather than a uniformly better or worse one.
//
// What to expect from that shape, and why the suite runs both micro and
// analytical workloads against it: a point read has to find one row in a
// column store and pays for an index probe plus a row reconstruction,
// where SQLite's clustered primary key is the row. A scan or an aggregate
// over the whole node table is the case DuckDB is built for and reads
// columns at memory bandwidth. A multi-hop join is a hash join over
// vectors rather than a nested loop over B-tree ranges, so it starts
// behind on a single seed and catches up as the frontier grows.
//
// # Two engines, two modes
//
//	duckdb      a database file on disk, which is what the storage
//	            footprint is measured from.
//	duckdb-mem  the database lives in memory, the ceiling the file mode
//	            is read against.
//
// Nothing else differs between them, and no setting below is changed
// between them either.
//
// # What is set, and why it is not tuning
//
// Nothing. DuckDB sizes its thread pool to the machine's cores and its
// memory limit to a share of RAM on its own, and those defaults are the
// configuration anyone benchmarking DuckDB would leave alone. The one
// knob worth naming is preserve_insertion_order, which speeds a bulk load
// and is deliberately left at its default, because turning it off is a
// change to what the engine guarantees and not just to how fast it runs.
//
// # The build tag
//
// go-duckdb links the DuckDB C library through cgo, and the prebuilt
// static library it carries is a large download, so the live half of this
// adapter is behind the duckdb tag and this file, the descriptor, builds
// everywhere. A binary without the tag still lists the engine and fails
// at Start with the build line.
package duckdb

import "github.com/tamnd/graph-bench/engine"

// Mode is where the database lives, one per registered engine name.
type Mode string

const (
	// File is a database file on disk.
	File Mode = "file"
	// Memory keeps the whole database in memory.
	Memory Mode = "mem"
)

// Engine is the DuckDB descriptor for one mode.
type Engine struct{ mode Mode }

// New returns the descriptors for both modes, in report order.
func New() []*Engine {
	return []*Engine{{mode: File}, {mode: Memory}}
}

var _ engine.Engine = (*Engine)(nil)

// Info reports the engine's identity and what it can actually do. The
// capabilities are the same as SQLite's for the same reason: one SQL text
// serves every relational engine, a recursive CTE is a variable-length
// path, a depth bound inside it is a path predicate, and a shortest path
// is that walk with a min over the depths the target was reached at.
func (e *Engine) Info() engine.Info {
	name := "duckdb"
	if e.mode != File {
		name = "duckdb-" + string(e.mode)
	}
	return engine.Info{
		Name:     name,
		Plane:    engine.InProc,
		Dialects: []engine.Dialect{engine.SQL},
		Caps: engine.Capabilities{
			Transactions:   true,
			BulkLoad:       true,
			Deletes:        true,
			VarLengthPaths: true,
			ShortestPaths:  true,
			PathPredicates: true,
			// DuckPGQ would add property-graph syntax and its own path
			// operators, but it is an extension this adapter does not
			// load, and no workload carries a sqlpgq text yet.
			Algorithms:     nil,
			MaxConcurrency: 0,
			Persistent:     e.mode == File,
		},
	}
}
