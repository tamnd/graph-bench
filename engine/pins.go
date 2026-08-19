package engine

// Pin records the version of an engine the suite is validated against.
// This table is the single source of truth (spec 04 §1): Docker files,
// CI, and docs must match it, and `graph-bench doctor` verifies they do.
// Live-reported versions (Session.Version) are what results stamp; a
// mismatch between pin and live version is surfaced by doctor, never
// silently accepted.
type Pin struct {
	Engine string
	Pinned string
	Source string // where the pin applies: "docker", "brew", "go.mod", "local"
}

// Pins is the v0.3.0 pin table.
var Pins = []Pin{
	{Engine: "zu", Pinned: "../zu local build (ZU_BIN)", Source: "local"},
	{Engine: "zu-capi", Pinned: "../zu local build (libzu, ZU_LIB)", Source: "local"},
	{Engine: "zu2", Pinned: "../zu local build (libzu2)", Source: "local"},
	{Engine: "neo4j", Pinned: "neo4j:2026.06.0", Source: "docker"},
	{Engine: "neo4j-go-driver", Pinned: "v6.2.0", Source: "go.mod"},
	{Engine: "ladybug", Pinned: "0.19.1", Source: "brew"},
	{Engine: "memgraph", Pinned: "memgraph/memgraph-mage:3.10.0", Source: "docker"},
	// SQLite has no separate install: go-sqlite3 carries the amalgamation
	// and compiles it in, so the driver version is what pins the engine
	// and the live version comes from sqlite_version(). All three modes
	// (sqlite, sqlite-sync, sqlite-mem) are the same library.
	{Engine: "sqlite", Pinned: "3.53.4 (go-sqlite3 v1.14.50)", Source: "go.mod"},
	{Engine: "sqlite-sync", Pinned: "3.53.4 (go-sqlite3 v1.14.50)", Source: "go.mod"},
	{Engine: "sqlite-mem", Pinned: "3.53.4 (go-sqlite3 v1.14.50)", Source: "go.mod"},
	// DuckDB is the same story: go-duckdb carries a prebuilt library and
	// the driver version is what pins the engine.
	{Engine: "duckdb", Pinned: "1.4.1 (go-duckdb v2.4.3)", Source: "go.mod"},
	{Engine: "duckdb-mem", Pinned: "1.4.1 (go-duckdb v2.4.3)", Source: "go.mod"},
	// PostgreSQL is a server, so the pin is the image the run verb starts
	// and setup.Postgres reads it from here. A run against an operator's
	// own server stamps that server's version instead, which is the whole
	// reason results carry a live version next to the pin.
	{Engine: "postgres", Pinned: "postgres:18.6", Source: "docker"},
	{Engine: "pgx", Pinned: "v5.10.0", Source: "go.mod"},
	// MongoDB is a server too, and setup.Mongo reads its image from here.
	{Engine: "mongodb", Pinned: "mongo:8.3.8", Source: "docker"},
	{Engine: "mongo-driver", Pinned: "v2.8.0", Source: "go.mod"},
}

// PinFor returns the pin for an engine name, if recorded.
func PinFor(name string) (Pin, bool) {
	for _, p := range Pins {
		if p.Engine == name {
			return p, true
		}
	}
	return Pin{}, false
}
