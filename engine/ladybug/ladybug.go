//go:build ladybug

// Package ladybug is the CGo adapter for LadybugDB, the Kùzu-fork embedded
// graph database (liblbug 0.19.1, spec 04 §4). It runs on the in-process
// plane: the database opens inside the harness process, bulk loads go
// through COPY FROM, and queries call the C API directly with no network
// hop.
//
// Build tag: ladybug. Compile with -tags ladybug.
//
// # Library location
//
// The default #cgo directives assume Homebrew paths (/opt/homebrew). CGo
// cannot read environment variables inside #cgo lines, so non-brew
// installs override the flags at build time instead, per spec 04 §4's
// LBUG_INCLUDE / LBUG_LIB seam:
//
//	CGO_CFLAGS="-I$LBUG_INCLUDE" \
//	CGO_LDFLAGS="$LBUG_LIB/liblbug.dylib" \
//	go build -tags ladybug ./...
//
// On darwin the library is named by its full path rather than with -L and
// -llbug, which looks fussy and is not. A -L is global to the link, not to
// this package, so putting /opt/homebrew/lib on the search path lets it
// answer every other package's -l as well. Homebrew ships its own DuckDB
// there, go-duckdb ships a different one in its module directory, and with
// -tags duckdb,ladybug the Homebrew copy won that race and the link failed
// on a missing duckdb::ExtensionHelper::LoadAllExtensions. Naming the file
// takes this package out of that argument entirely. Other platforms keep
// -L and -llbug, where no such collision has shown up yet.
//
// # Capability probes
//
// Capabilities.Transactions and Capabilities.Algorithms are probed live
// at Start, not hardcoded (v1 hardcoded Transactions:false; 0.19 restored
// the Kùzu transaction API). Start runs BEGIN TRANSACTION / ROLLBACK; if
// that succeeds the Session implements Begin, otherwise Begin returns
// engine.ErrNoTransactions. Algorithms are detected with one cheap
// `CALL show_functions()` scan for the algo-extension kernels
// (bfs, pagerank, wcc, sssp); on any probe failure the list stays empty.
// Info() reflects the most recent Start's probe results; before any Start
// it reports the spec defaults (Transactions:true, no algorithms).
package ladybug

// #cgo CFLAGS: -I/opt/homebrew/include
// #cgo darwin LDFLAGS: /opt/homebrew/lib/liblbug.dylib
// #cgo !darwin LDFLAGS: -L/opt/homebrew/lib -llbug
// #include "lbug.h"
// #include <stdlib.h>
import "C"

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/tamnd/graph-bench/engine"
)

func init() { engine.Register(New()) }

// Engine is the LadybugDB in-process engine descriptor.
type Engine struct {
	mu       sync.Mutex
	txProbed bool
	txOK     bool
	algos    []string
}

// New returns the LadybugDB engine descriptor. Nothing happens until Start.
func New() *Engine { return &Engine{} }

var _ engine.Engine = (*Engine)(nil)

// Info reports static identity. Transactions and Algorithms reflect the
// probe results of the most recent Start; before any Start, Transactions
// defaults to true per spec 04 §4 and Algorithms is empty.
func (e *Engine) Info() engine.Info {
	e.mu.Lock()
	tx := true
	if e.txProbed {
		tx = e.txOK
	}
	algos := append([]string(nil), e.algos...)
	e.mu.Unlock()
	return engine.Info{
		Name:     "ladybug",
		Plane:    engine.InProc,
		Dialects: []engine.Dialect{engine.KuzuCy, engine.Cypher},
		Caps: engine.Capabilities{
			Transactions:   tx,
			BulkLoad:       true,
			Deletes:        true,
			VarLengthPaths: true,
			ShortestPaths:  true,
			// Recursive patterns are evaluated as reachability, so a
			// predicate has to ride inside the expansion — and 0.19.1's
			// expansion filter reads only literals: a query parameter in it
			// is rejected by the binder, and a variable bound by an
			// enclosing WITH is out of scope. A windowed variable-length
			// traversal therefore has no correct spelling here.
			PathPredicates: false,
			Algorithms:     algos,
			MaxConcurrency: runtime.NumCPU(),
			Persistent:     true,
		},
	}
}

// Start opens a LadybugDB database at cfg "path" (a fresh temp directory
// when unset) and returns a Session bound to it. It probes transaction
// and algorithm support live and records the results on the Engine.
func (e *Engine) Start(ctx context.Context, cfg engine.Config) (engine.Session, error) {
	path := cfg.Get("path", "")
	if path == "" {
		tmp, err := os.MkdirTemp("", "ladybug-db-*")
		if err != nil {
			return nil, fmt.Errorf("ladybug: temp dir: %w", err)
		}
		// LadybugDB wants to create the database directory itself.
		os.Remove(tmp)
		path = tmp
	}

	sysCfg := C.lbug_default_system_config()
	var db C.lbug_database
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	if state := C.lbug_database_init(cpath, sysCfg, &db); state != C.LbugSuccess {
		return nil, fmt.Errorf("ladybug: database init %q failed", path)
	}

	var conn C.lbug_connection
	if state := C.lbug_connection_init(&db, &conn); state != C.LbugSuccess {
		C.lbug_database_destroy(&db)
		return nil, fmt.Errorf("ladybug: connection init failed")
	}

	s := &session{db: db, conn: conn, path: path}
	s.txOK = s.probeTransactions()
	s.loadAlgoExtension()
	algos := s.probeAlgorithms()

	e.mu.Lock()
	e.txProbed = true
	e.txOK = s.txOK
	e.algos = algos
	e.mu.Unlock()

	return s, nil
}

// session is a live handle to an open LadybugDB database. One connection,
// serialized by mu: LadybugDB parallelizes each query internally across
// worker threads, so a single connection saturates the machine; mu also
// guards the prepared-statement cache and the bind/execute pair.
type session struct {
	mu     sync.Mutex
	db     C.lbug_database
	conn   C.lbug_connection
	path   string
	txOK   bool
	closed bool
	// prep caches one compiled prepared statement per query text. Compile
	// (parse, bind, plan) dominates small queries; the runner replays the
	// same text with different bound params, which is exactly what a
	// prepared statement is for.
	prep map[string]*C.lbug_prepared_statement
}

var _ engine.Session = (*session)(nil)

// probeTransactions reports whether the engine accepts explicit
// transactions, by trying BEGIN TRANSACTION and rolling back.
func (s *session) probeTransactions() bool {
	if err := s.execDiscard("BEGIN TRANSACTION"); err != nil {
		return false
	}
	return s.execDiscard("ROLLBACK") == nil
}

// loadAlgoExtension makes the graph kernels visible before they are probed.
// They live in an official optional extension that ships separately from the
// core library, so without this the engine reports no algorithms and every
// analytical workload SKIPs — a capability the engine has, scored as one it
// does not.
//
// This is the vendor's documented way to reach those kernels and it changes
// no execution setting, so it stays inside "untuned defaults". Both
// statements are best-effort: INSTALL needs the network on first use and
// caches afterwards, and a build with the kernels already linked in rejects
// the install while still answering the probe. A failure here is not an
// error — it lands as an empty algorithm list, and the analytical queries
// SKIP with a reason, which is the honest outcome.
func (s *session) loadAlgoExtension() {
	_ = s.execDiscard("INSTALL algo")
	_ = s.execDiscard("LOAD algo")
}

// probeAlgorithms scans show_functions() for the algo-extension kernels.
// Any failure yields an empty list: a SKIP is respectable, a fake
// capability is not.
func (s *session) probeAlgorithms() []string {
	res, err := s.query("CALL show_functions() RETURN *")
	if err != nil {
		return nil
	}
	defer res.Close()
	found := map[string]bool{}
	for res.Next() {
		row := res.Row()
		if len(row) == 0 {
			continue
		}
		name, ok := row[0].(string)
		if !ok {
			continue
		}
		switch n := strings.ToLower(name); {
		case n == "bfs":
			found["bfs"] = true
		case n == "page_rank" || n == "pagerank":
			found["pagerank"] = true
		case n == "wcc" || strings.Contains(n, "weakly_connected"):
			found["wcc"] = true
		case n == "sssp" || strings.Contains(n, "single_sssp"):
			found["sssp"] = true
		}
	}
	var algos []string
	for _, k := range []string{"bfs", "pagerank", "wcc", "sssp"} {
		if found[k] {
			algos = append(algos, k)
		}
	}
	return algos
}

// Version reports the linked liblbug version live.
func (s *session) Version(ctx context.Context) (string, error) {
	v := C.lbug_get_version()
	if v == nil {
		return "unknown", nil
	}
	defer C.lbug_destroy_string(v)
	return C.GoString(v), nil
}

// Load bulk-loads a dataset. File-backed datasets get DDL generated from
// the schema and COPY FROM per file (Method "copy"); statements-only
// datasets run their statements (Method "statements").
func (s *session) Load(ctx context.Context, ds engine.Dataset) (engine.LoadStats, error) {
	var stats engine.LoadStats
	var err error
	if ds.Dir() == "" {
		stats, err = s.loadStatements(ctx, ds)
	} else {
		stats, err = s.loadCopy(ctx, ds)
	}
	if err != nil {
		return engine.LoadStats{}, err
	}
	if m := ds.Manifest(); m != nil {
		stats.Nodes = m.Invariants.NodeCount
		stats.Edges = m.Invariants.EdgeCount
	}
	s.projectGraph(ds)
	stats.BytesOnDisk = dirSize(s.path)
	return stats, nil
}

// projectedGraph is the name the analytical query texts call the graph by.
// The kernels take a projection rather than the database, so the name has to
// be agreed on between the adapter and the workload text; it is spelled here
// and in the engine.KuzuCy texts of the galytics and gap workloads.
const projectedGraph = "gb"

// projectGraph names the loaded tables as a graph so the kernels can be
// called. The kernels do not run against the database directly — they take a
// named projection — and there is no way to create one from inside the query
// text that calls them, so it happens here, at the end of the load, where the
// table names are known.
//
// It covers every node and rel table in the dataset, which is what the
// analytical workloads mean by "the graph". Best-effort by design: a build
// without the algo extension has no project_graph at all, and there the
// analytical queries already SKIP on the missing kernel, so an error here
// would be reporting the same absence twice.
func (s *session) projectGraph(ds engine.Dataset) {
	schema := ds.Schema()
	nodes := sortedKeys(schema.Nodes)
	rels := sortedKeys(schema.Rels)
	if len(nodes) == 0 || len(rels) == 0 {
		return
	}
	quote := func(names []string) string {
		q := make([]string, len(names))
		for i, n := range names {
			q[i] = "'" + n + "'"
		}
		return "[" + strings.Join(q, ", ") + "]"
	}
	_ = s.execDiscard(fmt.Sprintf("CALL project_graph('%s', %s, %s)",
		projectedGraph, quote(nodes), quote(rels)))
}

func (s *session) loadStatements(ctx context.Context, ds engine.Dataset) (engine.LoadStats, error) {
	start := time.Now()
	for i, stmt := range ds.Statements() {
		if err := ctx.Err(); err != nil {
			return engine.LoadStats{}, err
		}
		if err := s.execDiscard(stmt); err != nil {
			return engine.LoadStats{}, fmt.Errorf("ladybug: load statement %d: %w", i, err)
		}
	}
	return engine.LoadStats{Duration: time.Since(start), Method: "statements"}, nil
}

func (s *session) loadCopy(ctx context.Context, ds engine.Dataset) (engine.LoadStats, error) {
	start := time.Now()
	schema := ds.Schema()

	// Node tables first (rel DDL references them), in sorted order so the
	// load is deterministic.
	for _, label := range sortedKeys(schema.Nodes) {
		if err := ctx.Err(); err != nil {
			return engine.LoadStats{}, err
		}
		ns := schema.Nodes[label]
		files, err := ds.NodeFiles(label)
		if err != nil {
			return engine.LoadStats{}, fmt.Errorf("ladybug: node files %s: %w", label, err)
		}
		if len(files) == 0 {
			continue
		}
		if err := s.execDiscard(buildNodeDDL(label, ns)); err != nil {
			return engine.LoadStats{}, fmt.Errorf("ladybug: create node table %s: %w", label, err)
		}
		for _, f := range files {
			if err := s.copyFile(label, f, writeStrippedNodeCSV); err != nil {
				return engine.LoadStats{}, err
			}
		}
	}

	for _, typ := range sortedKeys(schema.Rels) {
		if err := ctx.Err(); err != nil {
			return engine.LoadStats{}, err
		}
		rs := schema.Rels[typ]
		files, err := ds.RelFiles(typ)
		if err != nil {
			return engine.LoadStats{}, fmt.Errorf("ladybug: rel files %s: %w", typ, err)
		}
		if len(files) == 0 {
			continue
		}
		if err := s.execDiscard(buildRelDDL(typ, rs)); err != nil {
			return engine.LoadStats{}, fmt.Errorf("ladybug: create rel table %s: %w", typ, err)
		}
		for _, f := range files {
			if err := s.copyFile(typ, f, writeStrippedRelCSV); err != nil {
				return engine.LoadStats{}, err
			}
		}
	}

	return engine.LoadStats{Duration: time.Since(start), Method: "copy"}, nil
}

// copyFile rewrites src through strip and COPYs it into table.
func (s *session) copyFile(table, src string, strip func(string) (string, func(), error)) error {
	tmp, cleanup, err := strip(src)
	if err != nil {
		return fmt.Errorf("ladybug: strip csv %s: %w", filepath.Base(src), err)
	}
	defer cleanup()
	q := fmt.Sprintf("COPY %s FROM '%s' (HEADER=true)", table, tmp)
	if err := s.execDiscard(q); err != nil {
		return fmt.Errorf("ladybug: COPY %s from %s: %w", table, filepath.Base(src), err)
	}
	return nil
}

// Exec runs one operation. Empty params run the text directly; otherwise
// values bind through a cached prepared statement. A context deadline is
// applied as the connection query timeout, and cancellation interrupts
// the running query through the C API.
func (s *session) Exec(ctx context.Context, op engine.Op) (engine.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("ladybug: session closed")
	}
	s.applyDeadline(ctx)
	stop := s.watchCancel(ctx)
	defer stop()

	if len(op.Params) == 0 {
		return s.queryLocked(op.Text)
	}
	return s.execPreparedLocked(op.Text, op.Params)
}

// applyDeadline sets the connection query timeout from ctx (0 = none).
// The caller holds mu.
func (s *session) applyDeadline(ctx context.Context) {
	var ms uint64
	if dl, ok := ctx.Deadline(); ok {
		if rem := time.Until(dl); rem > 0 {
			ms = uint64(rem / time.Millisecond)
			if ms == 0 {
				ms = 1
			}
		} else {
			ms = 1
		}
	}
	C.lbug_connection_set_query_timeout(&s.conn, C.uint64_t(ms))
}

// watchCancel interrupts the connection when ctx is canceled. The
// returned stop func must be called once the query has finished.
// lbug_connection_interrupt is safe to call from another goroutine.
func (s *session) watchCancel(ctx context.Context) func() {
	if ctx.Done() == nil {
		return func() {}
	}
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			C.lbug_connection_interrupt(&s.conn)
		case <-stop:
		}
	}()
	return func() { close(stop) }
}

// query runs text under the session lock and returns its result.
func (s *session) query(text string) (engine.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queryLocked(text)
}

// execDiscard runs a statement and discards the result.
func (s *session) execDiscard(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.queryLocked(text)
	if err != nil {
		return err
	}
	return res.Close()
}

// queryLocked runs text on the connection. The caller holds mu.
func (s *session) queryLocked(text string) (*result, error) {
	cq := C.CString(text)
	defer C.free(unsafe.Pointer(cq))
	var qr C.lbug_query_result
	if state := C.lbug_connection_query(&s.conn, cq, &qr); state != C.LbugSuccess {
		return nil, queryResultError(&qr)
	}
	if !C.lbug_query_result_is_success(&qr) {
		return nil, queryResultError(&qr)
	}
	return newResult(qr), nil
}

// execPreparedLocked binds params on the cached statement for text and
// executes it. The caller holds mu.
func (s *session) execPreparedLocked(text string, params map[string]engine.Value) (*result, error) {
	stmt, err := s.preparedLocked(text)
	if err != nil {
		return nil, err
	}
	for name, val := range params {
		if err := bindParam(stmt, name, val); err != nil {
			return nil, fmt.Errorf("ladybug: bind %s: %w", name, err)
		}
	}
	var qr C.lbug_query_result
	if state := C.lbug_connection_execute(&s.conn, stmt, &qr); state != C.LbugSuccess {
		return nil, queryResultError(&qr)
	}
	if !C.lbug_query_result_is_success(&qr) {
		return nil, queryResultError(&qr)
	}
	return newResult(qr), nil
}

// preparedLocked returns the cached compiled statement for text,
// compiling on first use. The caller holds mu.
func (s *session) preparedLocked(text string) (*C.lbug_prepared_statement, error) {
	if stmt, ok := s.prep[text]; ok {
		return stmt, nil
	}
	stmt := new(C.lbug_prepared_statement)
	cq := C.CString(text)
	defer C.free(unsafe.Pointer(cq))
	if state := C.lbug_connection_prepare(&s.conn, cq, stmt); state != C.LbugSuccess {
		C.lbug_prepared_statement_destroy(stmt)
		return nil, fmt.Errorf("ladybug: prepare failed")
	}
	if !C.lbug_prepared_statement_is_success(stmt) {
		msg := C.lbug_prepared_statement_get_error_message(stmt)
		err := fmt.Errorf("ladybug: prepare: %s", C.GoString(msg))
		C.lbug_destroy_string(msg)
		C.lbug_prepared_statement_destroy(stmt)
		return nil, err
	}
	if s.prep == nil {
		s.prep = make(map[string]*C.lbug_prepared_statement)
	}
	s.prep[text] = stmt
	return stmt, nil
}

// queryResultError extracts the error message and destroys the result.
func queryResultError(qr *C.lbug_query_result) error {
	msg := C.lbug_query_result_get_error_message(qr)
	err := fmt.Errorf("ladybug: %s", C.GoString(msg))
	C.lbug_destroy_string(msg)
	C.lbug_query_result_destroy(qr)
	return err
}

func bindParam(stmt *C.lbug_prepared_statement, name string, val engine.Value) error {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	switch v := val.(type) {
	case nil:
		nv := C.lbug_value_create_null()
		C.lbug_prepared_statement_bind_value(stmt, cname, nv)
		C.lbug_value_destroy(nv)
	case bool:
		C.lbug_prepared_statement_bind_bool(stmt, cname, C.bool(v))
	case int:
		C.lbug_prepared_statement_bind_int64(stmt, cname, C.int64_t(v))
	case int32:
		C.lbug_prepared_statement_bind_int32(stmt, cname, C.int32_t(v))
	case int64:
		C.lbug_prepared_statement_bind_int64(stmt, cname, C.int64_t(v))
	case float32:
		C.lbug_prepared_statement_bind_float(stmt, cname, C.float(v))
	case float64:
		C.lbug_prepared_statement_bind_double(stmt, cname, C.double(v))
	case string:
		cs := C.CString(v)
		defer C.free(unsafe.Pointer(cs))
		C.lbug_prepared_statement_bind_string(stmt, cname, cs)
	case time.Time:
		C.lbug_prepared_statement_bind_timestamp(stmt, cname,
			C.lbug_timestamp_t{value: C.int64_t(v.UnixMicro())})
	default:
		cs := C.CString(fmt.Sprintf("%v", v))
		defer C.free(unsafe.Pointer(cs))
		C.lbug_prepared_statement_bind_string(stmt, cname, cs)
	}
	return nil
}

// Begin opens an explicit transaction when the Start probe succeeded,
// else returns engine.ErrNoTransactions. The transaction runs on the
// session's connection via BEGIN/COMMIT/ROLLBACK statements; the session
// lock serializes it against concurrent Exec calls per statement.
func (s *session) Begin(ctx context.Context, mode engine.AccessMode) (engine.Tx, error) {
	if !s.txOK {
		return nil, engine.ErrNoTransactions
	}
	stmt := "BEGIN TRANSACTION"
	if mode == engine.ReadMode {
		stmt = "BEGIN TRANSACTION READ ONLY"
	}
	if err := s.execDiscard(stmt); err != nil {
		return nil, fmt.Errorf("ladybug: begin: %w", err)
	}
	return &tx{s: s}, nil
}

// Close destroys the prepared statements, connection, and database. It is
// idempotent and safe on a partially failed session.
func (s *session) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	for _, stmt := range s.prep {
		C.lbug_prepared_statement_destroy(stmt)
	}
	s.prep = nil
	C.lbug_connection_destroy(&s.conn)
	C.lbug_database_destroy(&s.db)
	return nil
}

// tx is an explicit transaction on the session connection.
type tx struct {
	s    *session
	done bool
}

var _ engine.Tx = (*tx)(nil)

func (t *tx) Exec(ctx context.Context, op engine.Op) (engine.Result, error) {
	if t.done {
		return nil, fmt.Errorf("ladybug: transaction finished")
	}
	return t.s.Exec(ctx, op)
}

func (t *tx) Commit(ctx context.Context) error {
	if t.done {
		return fmt.Errorf("ladybug: transaction finished")
	}
	t.done = true
	return t.s.execDiscard("COMMIT")
}

func (t *tx) Rollback(ctx context.Context) error {
	if t.done {
		return fmt.Errorf("ladybug: transaction finished")
	}
	t.done = true
	return t.s.execDiscard("ROLLBACK")
}

// result wraps a lbug_query_result as an engine.Result.
type result struct {
	qr      C.lbug_query_result
	cols    []string
	current []engine.Value
	err     error
	done    bool
	closed  bool
}

var _ engine.Result = (*result)(nil)

func newResult(qr C.lbug_query_result) *result {
	n := int(C.lbug_query_result_get_num_columns(&qr))
	cols := make([]string, n)
	for i := 0; i < n; i++ {
		var cname *C.char
		C.lbug_query_result_get_column_name(&qr, C.uint64_t(i), &cname)
		if cname != nil {
			cols[i] = C.GoString(cname)
			C.lbug_destroy_string(cname)
		}
	}
	return &result{qr: qr, cols: cols}
}

func (r *result) Columns() []string { return r.cols }

func (r *result) Next() bool {
	if r.done || r.err != nil || r.closed {
		return false
	}
	if !C.lbug_query_result_has_next(&r.qr) {
		r.done = true
		return false
	}
	var tuple C.lbug_flat_tuple
	if state := C.lbug_query_result_get_next(&r.qr, &tuple); state != C.LbugSuccess {
		r.err = fmt.Errorf("ladybug: get next failed")
		r.done = true
		return false
	}
	// Copy every value out now: the tuple memory is reused on the next
	// call to get_next.
	row := make([]engine.Value, len(r.cols))
	for i := range r.cols {
		var val C.lbug_value
		if state := C.lbug_flat_tuple_get_value(&tuple, C.uint64_t(i), &val); state != C.LbugSuccess {
			row[i] = nil
			continue
		}
		row[i] = extractValue(&val)
		C.lbug_value_destroy(&val)
	}
	C.lbug_flat_tuple_destroy(&tuple)
	r.current = row
	return true
}

func (r *result) Row() []engine.Value { return r.current }

func (r *result) Err() error { return r.err }

func (r *result) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	C.lbug_query_result_destroy(&r.qr)
	return nil
}

// extractValue decodes a C lbug_value into the canonical value model.
// The value is only valid for the duration of this call (tuple memory is
// reused), so everything is copied out.
func extractValue(val *C.lbug_value) engine.Value {
	if C.lbug_value_is_null(val) {
		return nil
	}
	var ltype C.lbug_logical_type
	C.lbug_value_get_data_type(val, &ltype)
	typeID := C.lbug_data_type_get_id(&ltype)
	C.lbug_data_type_destroy(&ltype)

	switch typeID {
	case C.LBUG_BOOL:
		var out C.bool
		C.lbug_value_get_bool(val, &out)
		return bool(out)
	case C.LBUG_INT8:
		var out C.int8_t
		C.lbug_value_get_int8(val, &out)
		return int64(out)
	case C.LBUG_INT16:
		var out C.int16_t
		C.lbug_value_get_int16(val, &out)
		return int64(out)
	case C.LBUG_INT32:
		var out C.int32_t
		C.lbug_value_get_int32(val, &out)
		return int64(out)
	case C.LBUG_INT64, C.LBUG_SERIAL:
		var out C.int64_t
		C.lbug_value_get_int64(val, &out)
		return int64(out)
	case C.LBUG_UINT8:
		var out C.uint8_t
		C.lbug_value_get_uint8(val, &out)
		return int64(out)
	case C.LBUG_UINT16:
		var out C.uint16_t
		C.lbug_value_get_uint16(val, &out)
		return int64(out)
	case C.LBUG_UINT32:
		var out C.uint32_t
		C.lbug_value_get_uint32(val, &out)
		return int64(out)
	case C.LBUG_UINT64:
		var out C.uint64_t
		C.lbug_value_get_uint64(val, &out)
		return int64(out)
	case C.LBUG_FLOAT:
		var out C.float
		C.lbug_value_get_float(val, &out)
		return float64(out)
	case C.LBUG_DOUBLE:
		var out C.double
		C.lbug_value_get_double(val, &out)
		return float64(out)
	case C.LBUG_STRING:
		return extractString(val)
	case C.LBUG_DATE:
		var out C.lbug_date_t
		C.lbug_value_get_date(val, &out)
		return time.Unix(int64(out.days)*86400, 0).UTC()
	case C.LBUG_TIMESTAMP:
		var out C.lbug_timestamp_t
		C.lbug_value_get_timestamp(val, &out)
		return time.UnixMicro(int64(out.value)).UTC()
	case C.LBUG_TIMESTAMP_TZ:
		var out C.lbug_timestamp_tz_t
		C.lbug_value_get_timestamp_tz(val, &out)
		return time.UnixMicro(int64(out.value)).UTC()
	case C.LBUG_TIMESTAMP_NS:
		var out C.lbug_timestamp_ns_t
		C.lbug_value_get_timestamp_ns(val, &out)
		return time.Unix(0, int64(out.value)).UTC()
	case C.LBUG_TIMESTAMP_MS:
		var out C.lbug_timestamp_ms_t
		C.lbug_value_get_timestamp_ms(val, &out)
		return time.UnixMilli(int64(out.value)).UTC()
	case C.LBUG_TIMESTAMP_SEC:
		var out C.lbug_timestamp_sec_t
		C.lbug_value_get_timestamp_sec(val, &out)
		return time.Unix(int64(out.value), 0).UTC()
	case C.LBUG_INTERNAL_ID:
		var out C.lbug_internal_id_t
		C.lbug_value_get_internal_id(val, &out)
		return internalID(out)
	case C.LBUG_LIST, C.LBUG_ARRAY:
		return extractList(val)
	case C.LBUG_NODE:
		return extractNode(val)
	case C.LBUG_REL:
		return extractRel(val)
	case C.LBUG_RECURSIVE_REL:
		return extractPath(val)
	case C.LBUG_STRUCT, C.LBUG_UNION:
		return extractStruct(val)
	case C.LBUG_MAP:
		return extractMap(val)
	default:
		// Interval, decimal, blob, uuid, and anything new fall back to
		// the engine's string rendering.
		out := C.lbug_value_to_string(val)
		if out == nil {
			return nil
		}
		s := C.GoString(out)
		C.lbug_destroy_string(out)
		return s
	}
}

func extractString(val *C.lbug_value) engine.Value {
	var out *C.char
	C.lbug_value_get_string(val, &out)
	if out == nil {
		return ""
	}
	s := C.GoString(out)
	C.lbug_destroy_string(out)
	return s
}

func internalID(id C.lbug_internal_id_t) string {
	return fmt.Sprintf("%d:%d", uint64(id.table_id), uint64(id.offset))
}

func extractList(val *C.lbug_value) engine.Value {
	var n C.uint64_t
	if C.lbug_value_get_list_size(val, &n) != C.LbugSuccess {
		return nil
	}
	out := make([]engine.Value, int(n))
	for i := range out {
		var elem C.lbug_value
		if C.lbug_value_get_list_element(val, C.uint64_t(i), &elem) != C.LbugSuccess {
			continue
		}
		out[i] = extractValue(&elem)
		C.lbug_value_destroy(&elem)
	}
	return out
}

func extractStruct(val *C.lbug_value) engine.Value {
	var n C.uint64_t
	if C.lbug_value_get_struct_num_fields(val, &n) != C.LbugSuccess {
		return nil
	}
	out := make(map[string]engine.Value, int(n))
	for i := 0; i < int(n); i++ {
		var cname *C.char
		if C.lbug_value_get_struct_field_name(val, C.uint64_t(i), &cname) != C.LbugSuccess {
			continue
		}
		name := C.GoString(cname)
		C.lbug_destroy_string(cname)
		var fv C.lbug_value
		if C.lbug_value_get_struct_field_value(val, C.uint64_t(i), &fv) != C.LbugSuccess {
			continue
		}
		out[name] = extractValue(&fv)
		C.lbug_value_destroy(&fv)
	}
	return out
}

func extractMap(val *C.lbug_value) engine.Value {
	var n C.uint64_t
	if C.lbug_value_get_map_size(val, &n) != C.LbugSuccess {
		return nil
	}
	out := make(map[string]engine.Value, int(n))
	for i := 0; i < int(n); i++ {
		var kv, vv C.lbug_value
		if C.lbug_value_get_map_key(val, C.uint64_t(i), &kv) != C.LbugSuccess {
			continue
		}
		key := fmt.Sprintf("%v", extractValue(&kv))
		C.lbug_value_destroy(&kv)
		if C.lbug_value_get_map_value(val, C.uint64_t(i), &vv) != C.LbugSuccess {
			continue
		}
		out[key] = extractValue(&vv)
		C.lbug_value_destroy(&vv)
	}
	return out
}

// extractIDString decodes an INTERNAL_ID-typed value into its "table:offset"
// string form.
func extractIDString(val *C.lbug_value) string {
	var out C.lbug_internal_id_t
	if C.lbug_value_get_internal_id(val, &out) != C.LbugSuccess {
		return ""
	}
	return internalID(out)
}

func extractNode(val *C.lbug_value) engine.Node {
	n := engine.Node{Props: map[string]engine.Value{}}
	var idv C.lbug_value
	if C.lbug_node_val_get_id_val(val, &idv) == C.LbugSuccess {
		n.ID = extractIDString(&idv)
		C.lbug_value_destroy(&idv)
	}
	var lblv C.lbug_value
	if C.lbug_node_val_get_label_val(val, &lblv) == C.LbugSuccess {
		if s, ok := extractString(&lblv).(string); ok && s != "" {
			n.Labels = []string{s}
		}
		C.lbug_value_destroy(&lblv)
	}
	var size C.uint64_t
	if C.lbug_node_val_get_property_size(val, &size) == C.LbugSuccess {
		for i := 0; i < int(size); i++ {
			var cname *C.char
			if C.lbug_node_val_get_property_name_at(val, C.uint64_t(i), &cname) != C.LbugSuccess {
				continue
			}
			name := C.GoString(cname)
			C.lbug_destroy_string(cname)
			var pv C.lbug_value
			if C.lbug_node_val_get_property_value_at(val, C.uint64_t(i), &pv) != C.LbugSuccess {
				continue
			}
			n.Props[name] = extractValue(&pv)
			C.lbug_value_destroy(&pv)
		}
	}
	return n
}

func extractRel(val *C.lbug_value) engine.Rel {
	r := engine.Rel{Props: map[string]engine.Value{}}
	var idv C.lbug_value
	if C.lbug_rel_val_get_id_val(val, &idv) == C.LbugSuccess {
		r.ID = extractIDString(&idv)
		C.lbug_value_destroy(&idv)
	}
	var srcv C.lbug_value
	if C.lbug_rel_val_get_src_id_val(val, &srcv) == C.LbugSuccess {
		r.Start = extractIDString(&srcv)
		C.lbug_value_destroy(&srcv)
	}
	var dstv C.lbug_value
	if C.lbug_rel_val_get_dst_id_val(val, &dstv) == C.LbugSuccess {
		r.End = extractIDString(&dstv)
		C.lbug_value_destroy(&dstv)
	}
	var lblv C.lbug_value
	if C.lbug_rel_val_get_label_val(val, &lblv) == C.LbugSuccess {
		if s, ok := extractString(&lblv).(string); ok {
			r.Type = s
		}
		C.lbug_value_destroy(&lblv)
	}
	var size C.uint64_t
	if C.lbug_rel_val_get_property_size(val, &size) == C.LbugSuccess {
		for i := 0; i < int(size); i++ {
			var cname *C.char
			if C.lbug_rel_val_get_property_name_at(val, C.uint64_t(i), &cname) != C.LbugSuccess {
				continue
			}
			name := C.GoString(cname)
			C.lbug_destroy_string(cname)
			var pv C.lbug_value
			if C.lbug_rel_val_get_property_value_at(val, C.uint64_t(i), &pv) != C.LbugSuccess {
				continue
			}
			r.Props[name] = extractValue(&pv)
			C.lbug_value_destroy(&pv)
		}
	}
	return r
}

func extractPath(val *C.lbug_value) engine.Value {
	p := engine.Path{}
	var nodes C.lbug_value
	if C.lbug_value_get_recursive_rel_node_list(val, &nodes) == C.LbugSuccess {
		if list, ok := extractList(&nodes).([]engine.Value); ok {
			for _, v := range list {
				if n, ok := v.(engine.Node); ok {
					p.Nodes = append(p.Nodes, n)
				}
			}
		}
		C.lbug_value_destroy(&nodes)
	}
	var rels C.lbug_value
	if C.lbug_value_get_recursive_rel_rel_list(val, &rels) == C.LbugSuccess {
		if list, ok := extractList(&rels).([]engine.Value); ok {
			for _, v := range list {
				if r, ok := v.(engine.Rel); ok {
					p.Rels = append(p.Rels, r)
				}
			}
		}
		C.lbug_value_destroy(&rels)
	}
	return p
}

// buildNodeDDL generates CREATE NODE TABLE from the schema: id column is
// INT64 PRIMARY KEY, properties map through mapType.
func buildNodeDDL(label string, ns engine.NodeSchema) string {
	idName := ns.ID.Name
	if idName == "" {
		idName = "id"
	}
	parts := []string{idName + " INT64"}
	for _, c := range ns.Properties {
		parts = append(parts, c.Name+" "+mapType(c.Type))
	}
	parts = append(parts, "PRIMARY KEY("+idName+")")
	return fmt.Sprintf("CREATE NODE TABLE %s (%s)", label, strings.Join(parts, ", "))
}

// buildRelDDL generates CREATE REL TABLE with FROM/TO labels and typed
// properties.
func buildRelDDL(typ string, rs engine.RelSchema) string {
	parts := []string{fmt.Sprintf("FROM %s TO %s", rs.Start, rs.End)}
	for _, c := range rs.Properties {
		parts = append(parts, c.Name+" "+mapType(c.Type))
	}
	return fmt.Sprintf("CREATE REL TABLE %s (%s)", typ, strings.Join(parts, ", "))
}

// mapType maps canonical dataset column types (engine/dataset.go) to
// LadybugDB DDL types.
func mapType(t string) string {
	switch strings.ToUpper(t) {
	case "ID", "INT64", "LONG":
		return "INT64"
	case "INT32", "INT", "INTEGER":
		return "INT32"
	case "FLOAT", "FLOAT32":
		return "FLOAT"
	case "DOUBLE", "FLOAT64":
		return "DOUBLE"
	case "BOOL", "BOOLEAN":
		return "BOOLEAN"
	case "DATE":
		return "DATE"
	case "DATETIME", "TIMESTAMP":
		return "TIMESTAMP"
	case "STRING[]":
		return "STRING[]"
	default:
		return "STRING"
	}
}

// headerCol is one parsed canonical CSV header cell ("id:ID", ":LABEL",
// "name:STRING").
type headerCol struct {
	name string // property name, "" for pure structural columns
	ann  string // annotation after the colon: ID, LABEL, TYPE, START_ID, END_ID, or a type
}

func parseHeader(cells []string) []headerCol {
	out := make([]headerCol, len(cells))
	for i, c := range cells {
		if j := strings.LastIndex(c, ":"); j >= 0 {
			out[i] = headerCol{name: c[:j], ann: strings.ToUpper(c[j+1:])}
		} else {
			out[i] = headerCol{name: c}
		}
	}
	return out
}

func isStructural(ann string) bool {
	switch ann {
	case "ID", "LABEL", "TYPE", "START_ID", "END_ID":
		return true
	}
	return false
}

// writeStrippedNodeCSV streams a canonical node CSV into a temp file with
// plain headers: the :ID column keeps its name (default "id") and moves
// first, :LABEL/:TYPE columns are dropped, property annotations are
// stripped. Returns the temp path and a cleanup func.
func writeStrippedNodeCSV(src string) (string, func(), error) {
	return rewriteCSV(src, "lbug-node-*.csv", func(hdr []headerCol) ([]int, []string, error) {
		idIdx := -1
		idName := "id"
		var idxs []int
		var names []string
		for i, h := range hdr {
			switch h.ann {
			case "ID":
				idIdx = i
				if h.name != "" {
					idName = h.name
				}
			case "LABEL", "TYPE":
				// dropped
			default:
				idxs = append(idxs, i)
				names = append(names, h.name)
			}
		}
		if idIdx < 0 {
			return nil, nil, fmt.Errorf("no :ID column")
		}
		return append([]int{idIdx}, idxs...), append([]string{idName}, names...), nil
	})
}

// writeStrippedRelCSV streams a canonical rel CSV into a temp file:
// :START_ID becomes "from", :END_ID becomes "to" (first two columns, as
// COPY FROM requires), :TYPE/:LABEL are dropped, property annotations are
// stripped.
func writeStrippedRelCSV(src string) (string, func(), error) {
	return rewriteCSV(src, "lbug-rel-*.csv", func(hdr []headerCol) ([]int, []string, error) {
		fromIdx, toIdx := -1, -1
		var idxs []int
		var names []string
		for i, h := range hdr {
			switch h.ann {
			case "START_ID":
				fromIdx = i
			case "END_ID":
				toIdx = i
			case "TYPE", "LABEL":
				// dropped
			default:
				idxs = append(idxs, i)
				names = append(names, h.name)
			}
		}
		if fromIdx < 0 || toIdx < 0 {
			return nil, nil, fmt.Errorf("missing :START_ID or :END_ID column")
		}
		return append([]int{fromIdx, toIdx}, idxs...),
			append([]string{"from", "to"}, names...), nil
	})
}

// rewriteCSV streams src through a column selection into a temp CSV.
// pick maps the parsed header to (source column indices, output names).
func rewriteCSV(src, pattern string, pick func([]headerCol) ([]int, []string, error)) (string, func(), error) {
	nop := func() {}
	f, err := os.Open(src)
	if err != nil {
		return "", nop, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.ReuseRecord = true
	hdr, err := r.Read()
	if err != nil {
		return "", nop, fmt.Errorf("read header: %w", err)
	}
	idxs, names, err := pick(parseHeader(hdr))
	if err != nil {
		return "", nop, err
	}

	tmp, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", nop, fmt.Errorf("temp file: %w", err)
	}
	name := tmp.Name()
	cleanup := func() { os.Remove(name) }
	fail := func(e error) (string, func(), error) {
		tmp.Close()
		cleanup()
		return "", nop, e
	}

	w := csv.NewWriter(tmp)
	if err := w.Write(names); err != nil {
		return fail(err)
	}
	out := make([]string, len(idxs))
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fail(fmt.Errorf("read csv: %w", err))
		}
		for i, idx := range idxs {
			if idx < len(row) {
				out[i] = row[idx]
			} else {
				out[i] = ""
			}
		}
		if err := w.Write(out); err != nil {
			return fail(err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	return name, cleanup, nil
}

// dirSize sums the file sizes under path, -1 on error.
func dirSize(path string) int64 {
	var total int64
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return -1
	}
	return total
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
