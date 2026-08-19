// Package sqlbase is the half of a relational adapter that has nothing to
// do with which relational engine it is. SQLite, DuckDB and PostgreSQL
// answer the same SQL text over the same two tables here, so the part
// that reads the dataset, rewrites parameters, runs a statement and
// decodes a row is written once and the three adapters supply only what
// actually differs: how to open, how to bulk load, what the DDL is, and
// where to read a size off disk.
//
// # The schema
//
// One node table and one edge table:
//
//	node(id BIGINT PRIMARY KEY)
//	edge(src BIGINT, dst BIGINT, PRIMARY KEY (src, dst))
//	index on edge(dst, src)
//
// That is the shape a competent engineer would build for adjacency in a
// relational store, and it is the one the SQL texts in the workloads are
// written against. The primary key is the out-adjacency, sorted by src so
// a neighbour list is one range scan; the secondary index is the
// in-adjacency, which is what an undirected walk needs and what costs a
// second copy of every edge on disk. Both facts are meant to be visible
// in the numbers.
//
// A dataset with more than one node label or more than one relationship
// type does not fit and is refused at Load rather than merged into these
// two tables, because merging would answer a different question than the
// one the workload asked. That is the same rule the zu2 adapter follows.
//
// # Parameters
//
// A workload's SQL text spells a parameter $name, as its Cypher spells
// it. Drivers do not agree on that: SQLite and DuckDB want ?, PostgreSQL
// wants $1. Rewrite turns one into the other and returns the arguments in
// the order the driver will read them, so a text stays a text and no
// driver's placeholder syntax leaks into a workload.
//
// # Column annotations
//
// A result column aliased "name::type" is named `name` and decoded as
// `type`. It exists because SQLite has no boolean: an existence probe
// there returns the integer 1 where the reference answer says true, and
// the comparison is by type, not by value. Only bool is annotated, and on
// an engine that already returns a boolean the annotation changes
// nothing.
package sqlbase

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// Driver is the engine-specific half of a relational adapter.
type Driver interface {
	// Name is the engine name, used in error messages.
	Name() string

	// Placeholder renders the driver's placeholder for the i'th argument,
	// counting from 1.
	Placeholder(i int) string

	// Schema is the DDL run before the load: the tables, and any index
	// that has to exist while rows arrive (a primary key usually does).
	Schema() []string

	// Indexes is the DDL run after the load, for indexes that are cheaper
	// to build over a full table than to maintain row by row.
	Indexes() []string

	// LoadNodes and LoadEdges insert the dataset's CSV files and report
	// how many rows they wrote. Rows arrive with the endpoint columns
	// already located by header suffix; see Nodes and Edges.
	LoadNodes(ctx context.Context, db *sql.DB, files []string) (int64, error)
	LoadEdges(ctx context.Context, db *sql.DB, files []string) (int64, error)

	// LoadMethod names the path LoadNodes and LoadEdges took, for the
	// LoadStats stamp: "copy", "insert-tx", "load-csv".
	LoadMethod() string

	// Analyze updates whatever statistics the planner reads. It runs once
	// after the indexes are built and before any query, because a plan
	// chosen from no statistics is not the plan the engine would use in
	// production and measuring it would be measuring the wrong thing.
	Analyze(ctx context.Context, db *sql.DB) error

	// Version reports the engine version live from the running engine.
	Version(ctx context.Context, db *sql.DB) (string, error)

	// DiskBytes is the size of everything the engine wrote for this
	// database, tables and indexes and journal alike, or -1 where the
	// number is not meaningful.
	DiskBytes(ctx context.Context, db *sql.DB) (int64, error)
}

// Session is the shared engine.Session over a *sql.DB.
type Session struct {
	drv     Driver
	db      *sql.DB
	cleanup func() error

	// stmts caches one *sql.Stmt per statement text. Without it every
	// Exec pays a parse and a plan: database/sql hands a query with
	// arguments straight to the driver, and the driver prepares, steps and
	// finalizes it. That cost is real but it is not the engine's storage,
	// and an application that ran this query a million times would have
	// prepared it once. An *sql.Stmt is pool-aware, preparing lazily on
	// whichever connection it lands on and reusing it after.
	stmts sync.Map // string -> *sql.Stmt

	nodes, edges int64
}

var _ engine.Session = (*Session)(nil)

// Open binds a session to an open database. cleanup runs at Close after
// the database is closed, and may be nil; it is where a temp directory
// gets removed or a container gets stopped.
func Open(drv Driver, db *sql.DB, cleanup func() error) *Session {
	return &Session{drv: drv, db: db, cleanup: cleanup}
}

// DB exposes the handle for a driver that needs it outside the session
// lifecycle, such as one measuring its own size.
func (s *Session) DB() *sql.DB { return s.db }

// Version reports the engine version live.
func (s *Session) Version(ctx context.Context) (string, error) {
	return s.drv.Version(ctx, s.db)
}

// Load creates the schema, inserts the dataset, builds the secondary
// index and updates the planner's statistics. Index build and analyze are
// inside the reported duration because they are part of what it costs to
// have a queryable database, and leaving them out would report a load
// that nobody could query.
func (s *Session) Load(ctx context.Context, ds engine.Dataset) (engine.LoadStats, error) {
	start := time.Now()
	nodeFiles, relFiles, err := singleTable(s.drv.Name(), ds)
	if err != nil {
		return engine.LoadStats{}, err
	}
	for _, stmt := range s.drv.Schema() {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return engine.LoadStats{}, fmt.Errorf("%s: schema %q: %w", s.drv.Name(), stmt, err)
		}
	}
	nodes, err := s.drv.LoadNodes(ctx, s.db, nodeFiles)
	if err != nil {
		return engine.LoadStats{}, fmt.Errorf("%s: load nodes: %w", s.drv.Name(), err)
	}
	edges, err := s.drv.LoadEdges(ctx, s.db, relFiles)
	if err != nil {
		return engine.LoadStats{}, fmt.Errorf("%s: load edges: %w", s.drv.Name(), err)
	}
	for _, stmt := range s.drv.Indexes() {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return engine.LoadStats{}, fmt.Errorf("%s: index %q: %w", s.drv.Name(), stmt, err)
		}
	}
	if err := s.drv.Analyze(ctx, s.db); err != nil {
		return engine.LoadStats{}, fmt.Errorf("%s: analyze: %w", s.drv.Name(), err)
	}
	elapsed := time.Since(start)
	s.nodes, s.edges = nodes, edges
	bytes, err := s.drv.DiskBytes(ctx, s.db)
	if err != nil {
		return engine.LoadStats{}, fmt.Errorf("%s: disk bytes: %w", s.drv.Name(), err)
	}
	return engine.LoadStats{
		Duration:    elapsed,
		Nodes:       nodes,
		Edges:       edges,
		BytesOnDisk: bytes,
		Method:      s.drv.LoadMethod(),
	}, nil
}

// Exec runs one statement and streams its rows.
func (s *Session) Exec(ctx context.Context, op engine.Op) (engine.Result, error) {
	if op.Dialect != engine.SQL {
		return nil, fmt.Errorf("%s: dialect %q is not SQL", s.drv.Name(), op.Dialect)
	}
	text, args, err := Rewrite(s.drv, op.Text, op.Params)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", s.drv.Name(), op.QueryID, err)
	}
	ctx = uncancellable(ctx)
	stmt, err := s.prepared(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", s.drv.Name(), op.QueryID, err)
	}
	rows, err := stmt.QueryContext(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", s.drv.Name(), op.QueryID, err)
	}
	return newResult(rows)
}

// prepared returns the cached statement for a text, preparing it the
// first time. Two callers racing on a new text both prepare and one
// closes its copy, which costs one parse and needs no lock.
func (s *Session) prepared(ctx context.Context, text string) (*sql.Stmt, error) {
	if cached, ok := s.stmts.Load(text); ok {
		return cached.(*sql.Stmt), nil
	}
	stmt, err := s.db.PrepareContext(ctx, text)
	if err != nil {
		return nil, err
	}
	if cached, loaded := s.stmts.LoadOrStore(text, stmt); loaded {
		stmt.Close()
		return cached.(*sql.Stmt), nil
	}
	return stmt, nil
}

// Begin opens an explicit transaction.
func (s *Session) Begin(ctx context.Context, mode engine.AccessMode) (engine.Tx, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: mode == engine.ReadMode})
	if err != nil {
		return nil, fmt.Errorf("%s: begin: %w", s.drv.Name(), err)
	}
	return &txn{drv: s.drv, tx: tx}, nil
}

// Close closes the database and runs the cleanup. It is idempotent and
// safe on a session that never loaded.
func (s *Session) Close(context.Context) error {
	var first error
	s.stmts.Range(func(key, value any) bool {
		if err := value.(*sql.Stmt).Close(); err != nil && first == nil {
			first = err
		}
		s.stmts.Delete(key)
		return true
	})
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			first = err
		}
		s.db = nil
	}
	if s.cleanup != nil {
		if err := s.cleanup(); err != nil && first == nil {
			first = err
		}
		s.cleanup = nil
	}
	return first
}

// txn is an explicit transaction over the same rewrite and decode path.
type txn struct {
	drv Driver
	tx  *sql.Tx
}

var _ engine.Tx = (*txn)(nil)

func (t *txn) Exec(ctx context.Context, op engine.Op) (engine.Result, error) {
	if op.Dialect != engine.SQL {
		return nil, fmt.Errorf("%s: dialect %q is not SQL", t.drv.Name(), op.Dialect)
	}
	text, args, err := Rewrite(t.drv, op.Text, op.Params)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", t.drv.Name(), op.QueryID, err)
	}
	rows, err := t.tx.QueryContext(uncancellable(ctx), text, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", t.drv.Name(), op.QueryID, err)
	}
	return newResult(rows)
}

func (t *txn) Commit(context.Context) error   { return t.tx.Commit() }
func (t *txn) Rollback(context.Context) error { return t.tx.Rollback() }

// uncancellable strips the cancellation from a query's context and keeps
// its values. It is here because of what database/sql does with a
// cancellable context: it starts a watcher goroutine per query and tears
// it down after, which measures at 3.6µs on this laptop against 1.6µs for
// the same query with an uncancellable one. On a read that costs a few
// microseconds that is most of the number, and it is Go's plumbing rather
// than the database's work.
//
// Nothing is lost that the other engines provide. zu, zu2 and Ladybug all
// run a query to completion whatever the context says, because a C call
// in flight is not interruptible; a cancelled run stops between
// operations for every engine here, and it still stops between operations
// for this one. Load keeps its cancellable context, where a cancellation
// has minutes to matter and the watcher costs nothing against them.
func uncancellable(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}

// Rewrite turns the harness's $name parameters into the driver's
// placeholders and returns the arguments in the order the driver reads
// them. A name that appears twice is passed twice rather than reused,
// which every driver accepts and which keeps the rewrite a single
// left-to-right pass.
func Rewrite(drv Driver, text string, params map[string]engine.Value) (string, []any, error) {
	var out strings.Builder
	out.Grow(len(text))
	var args []any
	for i := 0; i < len(text); {
		c := text[i]
		if c != '$' {
			out.WriteByte(c)
			i++
			continue
		}
		j := i + 1
		for j < len(text) && isNameByte(text[j]) {
			j++
		}
		if j == i+1 {
			return "", nil, fmt.Errorf("bare $ at offset %d", i)
		}
		name := text[i+1 : j]
		v, ok := params[name]
		if !ok {
			return "", nil, fmt.Errorf("no parameter %q was bound", name)
		}
		arg, err := argValue(v)
		if err != nil {
			return "", nil, fmt.Errorf("parameter %q: %w", name, err)
		}
		args = append(args, arg)
		out.WriteString(drv.Placeholder(len(args)))
		i = j
	}
	return out.String(), args, nil
}

func isNameByte(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}

// argValue converts a pool value into something a SQL driver takes. A
// key that is written as a string but reads as an integer is passed as
// an integer: the id columns are BIGINT, and a driver that sends "42" as
// text leaves the engine to compare a string against a number, which
// SQLite does by coercing and PostgreSQL does by refusing.
func argValue(v engine.Value) (any, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case string:
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			return n, nil
		}
		return t, nil
	case int:
		return int64(t), nil
	case int32:
		return int64(t), nil
	case int64:
		return t, nil
	case float64:
		return t, nil
	case bool:
		return t, nil
	default:
		return nil, fmt.Errorf("cannot bind %T", v)
	}
}

// singleTable checks the dataset fits the two-table schema and returns
// its node and relationship files.
func singleTable(name string, ds engine.Dataset) (nodeFiles, relFiles []string, err error) {
	if ds.Dir() == "" {
		return nil, nil, fmt.Errorf("%s: needs a file dataset; this one is statements only", name)
	}
	schema := ds.Schema()
	if len(schema.Nodes) != 1 {
		return nil, nil, fmt.Errorf("%s: the schema here is one node table, but the dataset has %d labels", name, len(schema.Nodes))
	}
	if len(schema.Rels) != 1 {
		return nil, nil, fmt.Errorf("%s: the schema here is one edge table, but the dataset has %d relationship types", name, len(schema.Rels))
	}
	for label := range schema.Nodes {
		if nodeFiles, err = ds.NodeFiles(label); err != nil {
			return nil, nil, fmt.Errorf("%s: node files for %q: %w", name, label, err)
		}
	}
	for typ := range schema.Rels {
		if relFiles, err = ds.RelFiles(typ); err != nil {
			return nil, nil, fmt.Errorf("%s: rel files for %q: %w", name, typ, err)
		}
	}
	return nodeFiles, relFiles, nil
}
