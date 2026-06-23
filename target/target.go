// Package target defines the service-provider interface that every graph engine
// plugs into: Target, Driver, Tx, and Result, plus the value model and the
// support types they exchange. It is the narrowest, most stable package in the
// harness; the workload and measurement layers are written against it and never
// against a specific engine.
//
// One Target is one engine on one plane. The lifecycle is strictly
// Setup -> Load -> (validate, warm, measure) -> Teardown, and Teardown always
// runs, even after a failed run. See notes/Spec/2060/bench/03-target-spi.md for
// the full contract and the seven guarantees an adapter must satisfy.
package target

import (
	"context"
	"time"
)

// Target is an engine that can be stood up, loaded with a dataset, queried, and
// torn down. One Target is one engine on one plane. gr appears as two Targets:
// gr in-process and gr over Bolt.
type Target interface {
	// Name is the stable identifier used in reports and the lineage, for
	// example "gr", "gr-bolt", "neo4j". It is unique per run.
	Name() string

	// Version returns the exact engine version, queried from the running engine
	// where possible rather than hard-coded, for the result stamp.
	Version(ctx context.Context) (string, error)

	// Plane reports which plane this Target uses: InProc, Bolt, or Native.
	Plane() Plane

	// Capabilities describes what the engine supports, so the harness can skip
	// a workload an engine cannot run rather than fail it.
	Capabilities() Capabilities

	// Setup stands the engine up against a fresh, empty database and returns a
	// Driver bound to it. config carries the declared, published per-engine
	// configuration.
	Setup(ctx context.Context, config Config) (Driver, error)

	// Load ingests a dataset through the engine's load path and returns the load
	// statistics. The adapter knows how its engine consumes the dataset.
	Load(ctx context.Context, d Driver, ds Dataset) (LoadStats, error)

	// Teardown closes the Driver and releases the engine's resources. It is
	// always called, even after a failed run, so it must be safe to call on a
	// partially constructed Driver.
	Teardown(ctx context.Context, d Driver) error
}

// Driver is a live handle to a set-up, loaded engine. It runs queries and
// manages transactions. The measurement layer holds a Driver and calls Run in a
// loop; it never touches the plane underneath.
type Driver interface {
	// Run executes one query and returns a Result. The query carries per-engine
	// text already resolved for this Target's plane and language.
	Run(ctx context.Context, q Query, params Params) (Result, error)

	// Begin starts an explicit transaction in the given mode. Engines without
	// measured explicit transactions may wrap autocommit; Capabilities reports
	// the truth.
	Begin(ctx context.Context, mode AccessMode) (Tx, error)

	// Close releases the Driver. Called by Teardown.
	Close(ctx context.Context) error
}

// Tx is an explicit transaction scope.
type Tx interface {
	Run(ctx context.Context, q Query, params Params) (Result, error)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// Result is a read-once, forward-only view of a query's output. The measurement
// layer consumes it to count rows for throughput and to materialize the answer
// for validation. It is engine-agnostic: a Bolt record and a gr record both
// present as a Result.
type Result interface {
	// Columns returns the result column names in order.
	Columns() []string

	// Next advances to the next row; false at end or on error.
	Next() bool

	// Row returns the current row as a slice of values aligned to Columns,
	// using the canonical value model.
	Row() []Value

	// Err returns any error encountered during iteration.
	Err() error

	// Close releases the result. Always called.
	Close() error
}

// Query is a single workload query resolved for a particular plane and language.
// The workload catalog holds the abstract query with texts for every plane; by
// the time a Query reaches a Driver, the right text is set.
type Query struct {
	ID    string // stable id, for example "snb-is2", "lsqb-q5", "micro-khop3"
	Class Class  // the budget class
	Text  string // the resolved query text for this Target's plane and language
	// Reference, if non-nil, is the engine-independent expected answer used for
	// validation; computed once per dataset.
	Reference *Answer
}

// Params are the bound parameters for a query, in the canonical value model.
type Params map[string]Value

// Value is the canonical cross-engine value: nil, bool, int64, float64, string,
// []byte, []Value, map[string]Value, or a graph object (Node, Relationship,
// Path). Every adapter maps its engine's values into this model so that
// validation compares like with like.
type Value = any

// Plane is the mechanism a Target is driven through.
type Plane int

const (
	InProc Plane = iota // embedded, in the harness process
	Bolt                // Bolt wire protocol, openCypher
	Native              // engine-native protocol and language
)

// String returns the plane's stable name for stamps and reports.
func (p Plane) String() string {
	switch p {
	case InProc:
		return "inproc"
	case Bolt:
		return "bolt"
	case Native:
		return "native"
	default:
		return "unknown"
	}
}

// Class is the budget class a query is measured against.
type Class int

const (
	PointRead  Class = iota // single-entity lookup
	Traversal               // bounded k-hop neighborhood
	Subgraph                // subgraph match
	Write                   // insert or delete
	Analytical              // large-scan aggregation, algorithms
)

// String returns the class's stable name.
func (c Class) String() string {
	switch c {
	case PointRead:
		return "point-read"
	case Traversal:
		return "traversal"
	case Subgraph:
		return "subgraph"
	case Write:
		return "write"
	case Analytical:
		return "analytical"
	default:
		return "unknown"
	}
}

// Language is a query language a Target may speak.
type Language string

const (
	Cypher Language = "cypher"
	SQLPGQ Language = "sql/pgq"
	SQL    Language = "sql"
	AQL    Language = "aql"
	GSQL   Language = "gsql"
)

// AccessMode is the transaction mode for Driver.Begin.
type AccessMode int

const (
	ReadMode  AccessMode = iota // snapshot-only, may not write
	WriteMode                   // read-write
)

// Capabilities describes what an engine supports, so the harness skips rather
// than fails a workload the engine cannot run.
type Capabilities struct {
	Languages      []Language // the languages the engine speaks
	Transactions   bool       // supports explicit Begin/Commit measured as such
	BulkCSVLoad    bool       // has a bulk CSV path (else loaded via queries)
	Algorithms     []string   // named graph algorithms it can run
	PersistentDisk bool       // persists to disk (vs purely in-memory)
}

// Config is the declared, published configuration for an engine on a run. It is
// captured verbatim into the result stamp; the adapter applies nothing that is
// not in it.
type Config struct {
	Values map[string]string // free-form per-engine settings, captured verbatim
	Tuned  bool              // a tuned run is always shown beside an out-of-the-box run
}

// LoadStats reports what a Load did.
type LoadStats struct {
	Duration    time.Duration
	BytesOnDisk int64 // -1 if not applicable (in-memory engines)
	Nodes       int64
	Edges       int64
}

// Dataset is the data a Target loads. The full dataset package (spec doc 04)
// implements the canonical CSV layout and the bulk-load accessors; this
// interface is the minimum the load path needs. Statements is the query-based
// load fallback for an engine without a bulk CSV path, and the path the
// in-process gr adapter uses until the CSV loader lands.
type Dataset interface {
	// Name is the stable dataset identifier, for example "micro-tiny" or "snb-sf1".
	Name() string

	// Statements returns the openCypher statements that build the graph, in
	// order, for engines loaded by issuing queries.
	Statements() []string
}

// Answer is the engine-independent expected result a query is validated against.
// The validator (spec doc 05 section 7) compares an engine's answer to it under
// the value model. It is a placeholder here and is filled in with the workload
// and validation milestones.
type Answer struct {
	Columns []string  // expected column names
	Rows    [][]Value // expected rows in the canonical value model
	// Unordered, when true, means the validator sorts both sides by a canonical
	// key before comparing rather than comparing row order.
	Unordered bool
	// FloatTolerance is the relative tolerance for float comparison; 0 means the
	// default (1e-9).
	FloatTolerance float64
}
