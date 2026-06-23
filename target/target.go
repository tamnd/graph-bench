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

// Dataset is a materialized, checksum-verified dataset in the canonical CSV
// layout. The dataset package constructs it; an adapter consumes it in Load.
// Both synthetic and LDBC datasets present as a Dataset. The schema and manifest
// types it returns live in this package so the contract is self-contained; the
// dataset package builds them.
type Dataset interface {
	// Name and Checksum identify the dataset for the result stamp.
	Name() string
	Checksum() string

	// Dir is the absolute path to the dataset directory (the one holding nodes/,
	// rels/, and manifest.json). An engine's bulk loader that takes a path is
	// pointed here. It is empty for a statements-only dataset.
	Dir() string

	// Manifest is the parsed manifest: counts, schema, seed, version, encoding.
	Manifest() *Manifest

	// Schema is the per-label and per-type description an adapter needs to build
	// load commands. It is Manifest().Schema for a materialized dataset.
	Schema() Schema

	// NodeFiles returns, for a label, the canonical node file paths (all shards,
	// in order) and the parsed typed header. RelFiles is the analog for a
	// relationship type.
	NodeFiles(label string) ([]string, []Column, error)
	RelFiles(typ string) ([]string, []Column, error)

	// Params returns the curated parameter set for a workload on this dataset,
	// so a workload runs the identical parameters on every engine. It returns
	// nil for a workload with no curated parameters.
	Params(workload string) (Params, error)

	// Statements returns openCypher statements that build the graph, for engines
	// loaded by issuing queries and for small test datasets. It is empty for a
	// dataset loaded from its CSV files.
	Statements() []string
}

// Manifest is the authority for what a dataset is: the reproduction recipe, the
// encoding conventions, the totals, the schema, and the content checksum. It
// mirrors the manifest.json in a dataset directory (spec doc 04 section 1.5).
type Manifest struct {
	Name             string         `json:"name"`
	Kind             string         `json:"kind"` // "synthetic" or "ldbc"
	Generator        string         `json:"generator,omitempty"`
	GeneratorVersion int            `json:"generatorVersion,omitempty"`
	Seed             int64          `json:"seed"`
	Params           map[string]any `json:"params,omitempty"`
	CreatedReference string         `json:"createdReference,omitempty"`
	ListDelimiter    string         `json:"listDelimiter"`
	Null             string         `json:"null"`
	Checksum         string         `json:"checksum"`
	NodeCount        int64          `json:"nodeCount"`
	EdgeCount        int64          `json:"edgeCount"`
	Schema           Schema         `json:"schema"`
	Invariants       Invariants     `json:"invariants"`
}

// Invariants are known ground-truth quantities a generator can carry, used as
// cheap validation fixtures. A nil pointer means the generator does not compute
// that invariant for these parameters.
type Invariants struct {
	NodeCount     *int64 `json:"nodeCount,omitempty"`
	EdgeCount     *int64 `json:"edgeCount,omitempty"`
	TriangleCount *int64 `json:"triangleCount,omitempty"`
	Diameter      *int64 `json:"diameter,omitempty"`
}

// Schema mirrors the manifest's schema block: the labels with their files and
// typed properties, and the relationship types with their endpoints.
type Schema struct {
	Nodes         map[string]NodeSchema `json:"nodes"`
	Relationships map[string]RelSchema  `json:"relationships"`
}

// NodeSchema describes one node label's files and columns.
type NodeSchema struct {
	Files      []string `json:"file"`
	ID         string   `json:"id"`
	Properties []Column `json:"properties"`
	Labels     []string `json:"labels"`
}

// RelSchema describes one relationship type's files, columns, and endpoints.
type RelSchema struct {
	Files      []string `json:"file"`
	Properties []Column `json:"properties"`
	Start      string   `json:"start"`
	End        string   `json:"end"`
}

// Column is one typed column in a canonical CSV header. Name is empty for the
// structural columns (:ID, :LABEL, :TYPE, :START_ID, :END_ID); Type carries the
// type token.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
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
