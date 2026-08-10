// Package engine defines the service provider interface every database
// adapter implements, the canonical value model results are decoded into,
// and the dialect-resolution rules. It is the leaf package of the harness:
// it imports nothing from graph-bench, and adapters import it plus their
// own SDK and nothing else.
//
// The contract is specified in notes/Spec/2064g/bench/03-engine-spi.md;
// the doc comments here are that contract.
package engine

import (
	"context"
	"slices"
	"time"
)

// Engine is a database under test. Implementations are cheap descriptors;
// nothing happens until Start.
type Engine interface {
	// Info is static identity: name, plane, dialect chain, capabilities.
	Info() Info

	// Start stands the engine up against a fresh, empty instance and
	// returns a Session bound to it. Config is engine-specific key/value
	// and is captured verbatim into the Condition stamp. Start must fail
	// loudly if the engine or its prerequisites are unreachable — never
	// degrade silently.
	Start(ctx context.Context, cfg Config) (Session, error)
}

// Session is a live connection to a started engine. The lifecycle is
// strictly: Load -> (Exec...) -> Close. Close is always called, must be
// idempotent, and must be safe on a partially failed Session.
type Session interface {
	// Version reports the engine version live from the running engine —
	// never from a pin.
	Version(ctx context.Context) (string, error)

	// Load bulk-loads the dataset using the engine's documented best
	// practice and reports what it cost.
	Load(ctx context.Context, ds Dataset) (LoadStats, error)

	// Exec runs one operation and returns its result stream. Exec must be
	// safe for concurrent use up to Info().Caps.MaxConcurrency.
	Exec(ctx context.Context, op Op) (Result, error)

	// Begin opens an explicit transaction. Sessions whose engine reports
	// Capabilities.Transactions == false return ErrNoTransactions.
	Begin(ctx context.Context, mode AccessMode) (Tx, error)

	Close(ctx context.Context) error
}

// Tx is an explicit transaction.
type Tx interface {
	Exec(ctx context.Context, op Op) (Result, error)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// Result is a forward-only, read-once row stream. Close releases engine
// resources and must always be called; the runner drains results before
// timing stops, so serialization cost is inside the measured region for
// every engine equally.
type Result interface {
	Columns() []string
	Next() bool
	Row() []Value
	Err() error
	Close() error
}

// Info is an engine's static identity.
type Info struct {
	// Name is the stable identifier used in flags, reports, and lineage
	// filenames: "zu", "neo4j", "ladybug", "memgraph".
	Name string

	// Plane is how the harness reaches the engine.
	Plane Plane

	// Dialects is the preference-ordered dialect chain used to resolve
	// workload query texts. First dialect with a text wins; none => SKIP.
	Dialects []Dialect

	// Caps declares what the engine can do. The runner filters operations
	// by capability before they reach the adapter; a SKIP is respectable,
	// a fake capability is not.
	Caps Capabilities
}

// Capabilities declares engine abilities the runner filters on.
type Capabilities struct {
	Transactions   bool
	BulkLoad       bool
	Deletes        bool
	VarLengthPaths bool
	ShortestPaths  bool

	// PathPredicates reports whether a variable-length pattern can be
	// constrained by a run-time predicate over the relationships it
	// traverses — Cypher's `all(r IN es WHERE r.ts >= $from)`.
	//
	// It is separate from VarLengthPaths because an engine can have one
	// without the other, and the gap is not cosmetic. An engine that
	// evaluates a variable-length pattern as reachability keeps one path per
	// endpoint pair, so a predicate applied after the expansion is not the
	// same question: a destination reachable by both an in-window and an
	// out-of-window path is kept or dropped depending on which path the
	// engine happened to retain. Such an engine must push the predicate into
	// the expansion, and if its expansion filter cannot read query
	// parameters, the windowed traversal has no correct spelling at all.
	// Declaring false makes those queries SKIP with a reason instead of
	// reporting a number that answers a narrower question.
	PathPredicates bool

	// Algorithms lists native whole-graph kernels reachable through the
	// adapter: "bfs", "pagerank", "wcc", "cdlp", "lcc", "sssp", "bc", "tc".
	Algorithms []string

	// MaxConcurrency bounds concurrent Exec calls on one Session.
	// 0 means unbounded (the runner applies its own worker limit).
	MaxConcurrency int

	// Persistent reports whether data survives a Session restart, which
	// cold-cache runs require.
	Persistent bool
}

// HasAlgorithm reports whether the engine declares the named kernel.
func (c Capabilities) HasAlgorithm(name string) bool {
	return slices.Contains(c.Algorithms, name)
}

// Config is engine-specific configuration, captured verbatim into the
// result's Condition stamp.
type Config struct {
	// Values holds engine-specific keys ("path", "uri", "pool", "bin").
	Values map[string]string

	// Tuned marks a run whose configuration goes beyond documented
	// defaults; tuned and untuned results are never compared silently.
	Tuned bool
}

// Get returns the value for key, or def when absent.
func (c Config) Get(key, def string) string {
	if v, ok := c.Values[key]; ok && v != "" {
		return v
	}
	return def
}

// Op is one operation the runner asks a Session to execute.
type Op struct {
	// QueryID is the workload query identifier ("is3", "bi14", "lb-add-link").
	QueryID string

	// Class buckets the operation for budgets and reporting.
	Class Class

	// Dialect names the dialect Text is written in, as resolved by the
	// runner against the engine's chain.
	Dialect Dialect

	// Text is the statement to execute.
	Text string

	// Params are bound parameters. Adapters for engines without parameter
	// binding interpolate and disclose it in the condition stamp.
	Params map[string]Value
}

// LoadStats reports what a bulk load cost.
type LoadStats struct {
	Duration time.Duration
	Nodes    int64
	Edges    int64

	// BytesOnDisk is the size of the engine's data files after load,
	// -1 where not meaningful.
	BytesOnDisk int64

	// Method names the load path used: "copy", "admin-import",
	// "load-csv", "unwind", "statements".
	Method string
}

// AccessMode selects transaction routing.
type AccessMode int

const (
	ReadMode AccessMode = iota
	WriteMode
)

// Class buckets queries for budgets and reporting.
type Class string

const (
	PointRead   Class = "point-read"
	Traversal   Class = "traversal"
	Subgraph    Class = "subgraph"
	Aggregation Class = "aggregation"
	Analytical  Class = "analytical"
	Write       Class = "write"
)

// Classes lists all classes in canonical report order.
func Classes() []Class {
	return []Class{PointRead, Traversal, Subgraph, Aggregation, Analytical, Write}
}

// Plane is how the harness reaches an engine.
type Plane string

const (
	// InProc runs the engine inside the harness process (Go library or CGo).
	InProc Plane = "inproc"
	// Bolt speaks the Bolt wire protocol through the official Go driver.
	Bolt Plane = "bolt"
	// Subprocess drives the engine's CLI over pipes with machine-readable
	// output; process spawn is never inside a timed region.
	Subprocess Plane = "subprocess"
	// Native speaks a non-Bolt wire (Postgres, HTTP, RESP) through the
	// engine's own driver.
	Native Plane = "native"
)
