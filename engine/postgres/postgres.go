// Package postgres is the graph-bench adapter for PostgreSQL, over the
// wire through jackc/pgx. It is the third relational engine and the first
// one that is not in the harness process, which is most of what its
// numbers say: the same query that costs SQLite a B-tree descent costs
// PostgreSQL a B-tree descent plus a round trip, and the round trip is
// larger. That is not a flaw in PostgreSQL and it is not a flaw in the
// measurement. It is the price of the architecture, and a graph workload
// that makes a million small queries pays it a million times, which is
// exactly the comparison this suite exists to make legible.
//
// # The plane
//
// Native rather than Bolt: PostgreSQL speaks its own wire protocol, and
// pgx speaks it directly rather than through libpq. The connection comes
// from Config["dsn"], else $GRAPH_BENCH_PG_DSN or $DATABASE_URL, else a
// managed container the run verb starts.
//
// # What is set, and why it is not tuning
//
// Nothing. The server runs on its own defaults, including fsync, which
// makes it the only engine here whose commits are durable by default in
// the strict sense. Raising shared_buffers would be defensible on a big
// dataset and is deliberately not done, because a claim that a number is
// untuned has to mean the server was untuned.
//
// # No build tag
//
// pgx is pure Go, so this adapter is in every build. What it needs is a
// server, and without one it fails at Start saying so.
package postgres

import "github.com/tamnd/graph-bench/engine"

// Engine is the PostgreSQL descriptor.
type Engine struct{}

// New returns the descriptor.
func New() *Engine { return &Engine{} }

var _ engine.Engine = (*Engine)(nil)

// Info reports the engine's identity and what it can actually do. The
// capabilities match the other relational engines because one set of SQL
// texts serves all of them: a recursive CTE is a variable-length path, a
// depth bound inside it is a path predicate, and a shortest path is that
// walk with a min over the depths the target was reached at.
func (e *Engine) Info() engine.Info {
	return engine.Info{
		Name:     "postgres",
		Plane:    engine.Native,
		Dialects: []engine.Dialect{engine.SQL},
		Caps: engine.Capabilities{
			Transactions:   true,
			BulkLoad:       true,
			Deletes:        true,
			VarLengthPaths: true,
			ShortestPaths:  true,
			PathPredicates: true,
			// Apache AGE would add openCypher and its own operators, but
			// it is an extension this adapter does not install, and a
			// stock PostgreSQL has no graph kernels to call.
			Algorithms:     nil,
			MaxConcurrency: 0,
			Persistent:     true,
		},
	}
}
