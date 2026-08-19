// Package sqlite is the graph-bench adapter for SQLite, in-process over
// the C library through mattn/go-sqlite3. It is the rival that matters
// most for a small embedded engine: same plane, same process, no wire, no
// server, and the storage layout a relational engine gives adjacency.
//
// # Three engines, three modes
//
// SQLite's durability setting changes its numbers by more than most
// engines differ from each other, so the mode is in the engine name
// rather than hidden in a config key. A report with two SQLite columns
// says which two on its face:
//
//	sqlite       WAL, synchronous=NORMAL. The common production setting
//	             and the fast default this suite quotes.
//	sqlite-sync  WAL, synchronous=FULL. Every commit waits for the disk.
//	sqlite-mem   the database lives in memory and never touches a disk,
//	             which is the ceiling the other two are read against.
//
// Everything else is identical between them, including the pragmas below,
// so the difference between two SQLite columns is exactly the mode.
//
// # What else is set, and why it is not tuning
//
// cache_size is 256 MiB, mmap_size is 256 MiB, temp_store is memory, and
// busy_timeout is 10 seconds. These are the settings the SQLite
// documentation recommends for a read-heavy database that fits in memory,
// they are what anyone benchmarking SQLite honestly would set, and
// leaving the 2 MiB default cache in place would measure page eviction
// rather than the engine. They are listed here because a setting nobody
// wrote down is a setting nobody can check.
//
// # The build tag
//
// go-sqlite3 compiles the SQLite amalgamation through cgo, so the live
// half of this adapter is behind the sqlite tag and this file, the
// descriptor, builds everywhere. A binary without the tag still lists the
// engine and fails at Start with the build line.
package sqlite

import "github.com/tamnd/graph-bench/engine"

// Mode is a durability and storage setting, one per registered engine
// name.
type Mode string

const (
	// WAL is the write-ahead log with synchronous=NORMAL: a commit is
	// durable across a process crash but not across a power cut.
	WAL Mode = "wal"
	// Sync is the write-ahead log with synchronous=FULL: every commit
	// fsyncs.
	Sync Mode = "sync"
	// Memory keeps the whole database in memory.
	Memory Mode = "mem"
)

// Engine is the SQLite descriptor for one mode.
type Engine struct{ mode Mode }

// New returns the descriptors for all three modes, in report order.
func New() []*Engine {
	return []*Engine{{mode: WAL}, {mode: Sync}, {mode: Memory}}
}

var _ engine.Engine = (*Engine)(nil)

// Info reports the engine's identity and what it can actually do. Every
// true here is something the SQL texts in the workloads exercise: a
// recursive CTE is a variable-length path, a bound on the depth inside
// that CTE is a path predicate, and the shortest path is that same walk
// with a min over the depths the target was reached at.
func (e *Engine) Info() engine.Info {
	name := "sqlite"
	if e.mode != WAL {
		name = "sqlite-" + string(e.mode)
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
			// No native kernels: a PageRank in SQLite is a query someone
			// writes, not a call the engine answers, and none of the
			// analytical workloads carry a SQL text for one.
			Algorithms:     nil,
			MaxConcurrency: 0,
			Persistent:     e.mode != Memory,
		},
	}
}
