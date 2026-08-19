// Engine registration for every build: zu and zu2 register here, and
// whether either can actually start depends on its build tag (zuinproc,
// zu2inproc), since both adapters link a C library. Registration is a
// cheap descriptor either way, and a binary built without the tag fails
// at Start with the build line to use rather than reporting an unknown
// engine. Engine construction reads its environment ($ZU_BIN and the rest
// of the discovery order, spec 09 §4) at Start.
//
// Bolt-plane adapters (neo4j, memgraph) register in engines_bolt.go under
// -tags bolt; the Ladybug in-process adapter self-registers via its own init
// and is blank-imported in engines_ladybug.go under -tags ladybug.
package main

import (
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/engine/duckdb"
	"github.com/tamnd/graph-bench/engine/mongo"
	"github.com/tamnd/graph-bench/engine/postgres"
	"github.com/tamnd/graph-bench/engine/sqlite"
	"github.com/tamnd/graph-bench/engine/zu"
	"github.com/tamnd/graph-bench/engine/zu2"
)

func init() {
	engine.Register(zu.New())
	engine.Register(zu2.New())
	// SQLite registers three times, one per durability mode, because the
	// mode changes its numbers more than most engines differ from each
	// other and a report column should say which one it is.
	for _, e := range sqlite.New() {
		engine.Register(e)
	}
	// DuckDB registers twice, on disk and in memory, for the same reason.
	for _, e := range duckdb.New() {
		engine.Register(e)
	}
	// PostgreSQL registers unconditionally because pgx is pure Go and needs
	// no build tag. What it needs is a server, and it says so at Start when
	// there is none.
	engine.Register(postgres.New())
	// MongoDB likewise: a pure Go driver, no tag, and a server it asks for
	// at Start.
	engine.Register(mongo.New())
}
