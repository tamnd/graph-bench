//go:build zuinproc

// The live half of the zu adapter, over libzu (crates/zu-capi): the
// database opens inside the harness process and every query is a direct
// call, with no frame, no pipe, and no child process in the timed
// region. That is the plane ladybug runs on, so this is the adapter that
// makes the two comparable.
//
// Build tag: zuinproc (spec 01 §3.2). The descriptor in zu.go builds
// without the tag and its Start says what to build, so the engine is
// never silently missing.
//
// # Library location
//
// The #cgo lines default to a sibling zu checkout built in release mode
// (../../../zu relative to this file, which is the layout the repos are
// developed in). cgo cannot read environment variables, so anything else
// overrides the flags at build time, the same seam the ladybug adapter
// documents:
//
//	CGO_CFLAGS="-I$ZU_INCLUDE" \
//	CGO_LDFLAGS="-L$ZU_LIB -lzu -Wl,-rpath,$ZU_LIB" \
//	go build -tags zuinproc ./...
//
// Build libzu first: cargo build --release -p zu-capi in the zu repo,
// which writes libzu.dylib (macOS) or libzu.so (Linux) into target/release.
//
// # What the C API does and does not cover
//
// zu.h covers open, prepare, bind, execute, and columnar result reads. It
// has no bulk-load entry point, so Load still shells out to `zu copy
// --reorder degree` and then opens the file it produced. Load is not in a
// timed region, so the plane claim stays honest: every measured operation
// here is a C call.
//
// # Threading
//
// A connection may move between threads but must not be used from two at
// once, and libzu answers ZU_MISUSE_CONCURRENT rather than corrupting a
// cache when it is. Exec is mutex serialized and
// Capabilities.MaxConcurrency stays 1, so calls never overlap. A
// connection holds no thread-local state, so serialized calls from
// different goroutines are fine. This adapter opens one connection with
// zu_open_z; when the harness runs concurrent operations it will open a
// database once and connect per worker instead.
package zu

// #cgo CFLAGS: -I${SRCDIR}/../../../zu/crates/zu-capi/include
// #cgo LDFLAGS: -L${SRCDIR}/../../../zu/target/release -lzu -Wl,-rpath,${SRCDIR}/../../../zu/target/release
// #include <stdlib.h>
// #include "zu.h"
import "C"

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"github.com/tamnd/graph-bench/engine"
)

// Start discovers the zu binary (Load needs the copy verb) and binds a
// session to a database file: Config["path"] if given, else "bench.zu1"
// in a fresh temp dir, removed on Close. The libzu session opens lazily,
// on the first query after the file exists.
func (e *Engine) Start(ctx context.Context, cfg engine.Config) (engine.Session, error) {
	bin, err := discoverBinary(cfg)
	if err != nil {
		return nil, err
	}
	workDir, err := os.MkdirTemp("", "graph-bench-zu-")
	if err != nil {
		return nil, fmt.Errorf("zu: create work dir: %w", err)
	}
	dbPath := cfg.Get("path", "")
	if dbPath == "" {
		dbPath = filepath.Join(workDir, "bench.zu1")
	}
	return &Session{bin: bin, dbPath: dbPath, workDir: workDir}, nil
}

// Session is one open libzu session plus its prepared-statement
// cache, keyed by query text the way a real embedding host would keep it.
type Session struct {
	bin     string
	dbPath  string
	workDir string

	mu     sync.Mutex
	sess   *C.zu_conn
	stmts  map[string]*C.zu_stmt
	closed bool
}

var _ engine.Session = (*Session)(nil)

// Bin reports the discovered binary path, for the condition stamp.
func (s *Session) Bin() string { return s.bin }

// Mode reports the exec surface, for the condition stamp. There is only
// one here: direct calls into libzu.
func (s *Session) Mode() string { return "capi" }

// Version reports the library version from zu_version(), which is the
// version of the code actually linked in, not of a binary on PATH.
func (s *Session) Version(ctx context.Context) (string, error) {
	return C.GoString(C.zu_version()), nil
}

// Begin opens an explicit transaction on the session connection, read
// only when the caller asked for read mode. The statements go through
// execDiscardLocked rather than the prepared-statement path, because a
// transaction statement is not a query and zu_prepare parses what it is
// given as one.
func (s *Session) Begin(ctx context.Context, mode engine.AccessMode) (engine.Tx, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stmt := "START TRANSACTION"
	if mode == engine.ReadMode {
		stmt = "START TRANSACTION READ ONLY"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.execDiscardLocked(stmt); err != nil {
		return nil, fmt.Errorf("zu: begin: %w", err)
	}
	return &tx{s: s}, nil
}

// tx is an explicit transaction on the session connection. zu holds the
// transaction on the connection itself, so a transaction is a marker
// around the same Exec path rather than a handle of its own.
type tx struct {
	s    *Session
	done bool
}

var _ engine.Tx = (*tx)(nil)

// Exec runs one operation inside the transaction.
func (t *tx) Exec(ctx context.Context, op engine.Op) (engine.Result, error) {
	if t.done {
		return nil, errors.New("zu: transaction finished")
	}
	return t.s.Exec(ctx, op)
}

// Commit makes the transaction's writes durable.
func (t *tx) Commit(ctx context.Context) error {
	return t.finish("COMMIT")
}

// Rollback throws the transaction's writes away.
func (t *tx) Rollback(ctx context.Context) error {
	return t.finish("ROLLBACK")
}

// finish ends the transaction one way or the other, once.
func (t *tx) finish(stmt string) error {
	if t.done {
		return errors.New("zu: transaction finished")
	}
	t.done = true
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	return t.s.execDiscardLocked(stmt)
}

// execDiscardLocked runs one statement for its effect and throws the
// result away. Caller holds s.mu.
//
// It goes through zu_query_z, the one-shot entry point, which routes
// through the session and so sees the transaction statements for what
// they are. zu_prepare_z cannot take them: it plans what it is handed as
// a query, and "START TRANSACTION" is not one.
func (s *Session) execDiscardLocked(stmt string) error {
	if s.closed {
		return errors.New("zu: session is closed")
	}
	if err := s.openLocked(); err != nil {
		return err
	}
	ctext := C.CString(stmt)
	defer C.free(unsafe.Pointer(ctext))
	var res *C.zu_result
	var cerr *C.zu_error
	if st := C.zu_query_z(s.sess, ctext, &res, &cerr); st != C.ZU_OK {
		return takeErr(st, cerr, fmt.Sprintf("zu: %q failed", stmt))
	}
	C.zu_result_free(res)
	return nil
}

// Calibrate reports the per-operation process-spawn floor, which is zero
// here: nothing spawns per operation. The number is still stamped, so a
// result file says on its face that the measurement carries no transport.
func (s *Session) Calibrate(ctx context.Context) time.Duration { return 0 }

// Load bulk-loads a file dataset through the CLI, materializing the
// canonical rel files into one edge list and running
// `zu copy --reorder degree`, edge properties included, then drops any
// open session so the next query opens the file that copy just wrote.
// libzu has no load entry point; when it grows one this moves in-process
// too. Load is outside every timed region either way.
//
// A statements-only dataset has no directory to copy, and goes through
// loadStatements instead: one Exec per setup statement, which is a write
// surface libzu did not have until zu's INSERT landed.
func (s *Session) Load(ctx context.Context, ds engine.Dataset) (engine.LoadStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return engine.LoadStats{}, errors.New("zu: session is closed")
	}
	if ds.Dir() == "" {
		return s.loadStatementsLocked(ctx, ds)
	}

	stats, err := copyDataset(ctx, "zu", s.bin, s.workDir, s.dbPath, ds)
	if err != nil {
		return engine.LoadStats{}, err
	}
	s.closeSessionLocked()
	return stats, nil
}

// loadStatementsLocked seeds a statements-only dataset by executing each
// setup statement through libzu. Caller holds s.mu, so it runs the
// statements through execLocked rather than Exec, which would deadlock.
func (s *Session) loadStatementsLocked(ctx context.Context, ds engine.Dataset) (engine.LoadStats, error) {
	start := time.Now()
	for i, stmt := range ds.Statements() {
		res, err := s.execLocked(engine.Op{
			QueryID: fmt.Sprintf("load-statement-%d", i),
			Class:   engine.Write,
			Dialect: engine.ZuQL,
			Text:    stmt,
		})
		if err != nil {
			return engine.LoadStats{}, fmt.Errorf("zu: load statement %d: %w", i, err)
		}
		for res.Next() {
		}
		errStream := res.Err()
		res.Close()
		if errStream != nil {
			return engine.LoadStats{}, fmt.Errorf("zu: load statement %d: %w", i, errStream)
		}
	}
	bytes := int64(-1)
	if fi, err := os.Stat(s.dbPath); err == nil {
		bytes = fi.Size()
	}
	return engine.LoadStats{
		Duration:    time.Since(start),
		BytesOnDisk: bytes,
		Method:      "statements",
	}, nil
}

// Exec runs one operation as a prepared statement: compile on the first
// sighting of the text, bind the parameters, execute. Mutex serialized
// (MaxConcurrency 1). libzu has no cancellation, so the context is
// checked on the way in and not again until the call returns.
func (s *Session) Exec(ctx context.Context, op engine.Op) (engine.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.execLocked(op)
}

// execLocked is Exec with the lock already held, so Load can run setup
// statements without releasing it between them. Caller holds s.mu.
func (s *Session) execLocked(op engine.Op) (engine.Result, error) {
	if s.closed {
		return nil, errors.New("zu: session is closed")
	}
	if err := s.openLocked(); err != nil {
		return nil, err
	}
	stmt, err := s.preparedLocked(op.Text)
	if err != nil {
		return nil, err
	}
	if err := bindParams(stmt, op.Params); err != nil {
		return nil, err
	}

	var res *C.zu_result
	var cerr *C.zu_error
	if st := C.zu_execute(stmt, &res, &cerr); st != C.ZU_OK {
		return nil, takeErr(st, cerr, "zu: execute failed")
	}
	defer C.zu_result_free(res)
	return decodeResult(res)
}

// Close frees the prepared statements, closes the session, and removes
// the work dir. Statements go first: the header requires it. Idempotent.
func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.closeSessionLocked()
	if s.workDir != "" {
		os.RemoveAll(s.workDir)
	}
	return nil
}

// openLocked opens the session on first use. Caller holds s.mu.
func (s *Session) openLocked() error {
	if s.sess != nil {
		return nil
	}
	cpath := C.CString(s.dbPath)
	defer C.free(unsafe.Pointer(cpath))
	var conn *C.zu_conn
	var cerr *C.zu_error
	if st := C.zu_open_z(cpath, &conn, &cerr); st != C.ZU_OK {
		return takeErr(st, cerr, fmt.Sprintf("zu: open %q failed", s.dbPath))
	}
	s.sess = conn
	return nil
}

// closeSessionLocked tears down statements then the session, leaving the
// struct ready to reopen. Caller holds s.mu.
func (s *Session) closeSessionLocked() {
	for _, stmt := range s.stmts {
		C.zu_stmt_close(stmt)
	}
	s.stmts = nil
	if s.sess != nil {
		C.zu_conn_close(s.sess)
		s.sess = nil
	}
}

// preparedLocked returns the cached statement for text, compiling it on
// first sighting. Caller holds s.mu.
func (s *Session) preparedLocked(text string) (*C.zu_stmt, error) {
	if stmt, ok := s.stmts[text]; ok {
		return stmt, nil
	}
	ctext := C.CString(text)
	defer C.free(unsafe.Pointer(ctext))
	var stmt *C.zu_stmt
	var cerr *C.zu_error
	if st := C.zu_prepare_z(s.sess, ctext, &stmt, &cerr); st != C.ZU_OK {
		return nil, takeErr(st, cerr, "zu: prepare failed")
	}
	if s.stmts == nil {
		s.stmts = make(map[string]*C.zu_stmt)
	}
	s.stmts[text] = stmt
	return stmt, nil
}

// bindParams binds every parameter by name. Bindings live on the
// statement across executions, so a repeated query only rebinds what
// changed; rebinding a name replaces the old value.
func bindParams(stmt *C.zu_stmt, params map[string]engine.Value) error {
	for name, val := range params {
		cname := C.CString(name)
		var st C.zu_status
		switch v := val.(type) {
		case nil:
			st = C.zu_bind_null_z(stmt, cname)
		case int:
			st = C.zu_bind_i64_z(stmt, cname, C.int64_t(v))
		case int32:
			st = C.zu_bind_i64_z(stmt, cname, C.int64_t(v))
		case int64:
			st = C.zu_bind_i64_z(stmt, cname, C.int64_t(v))
		case float32:
			st = C.zu_bind_f64_z(stmt, cname, C.double(v))
		case float64:
			st = C.zu_bind_f64_z(stmt, cname, C.double(v))
		case string:
			cv := C.CString(v)
			st = C.zu_bind_str_z(stmt, cname, cv)
			C.free(unsafe.Pointer(cv))
		case bool:
			// The C side has no bool of its own, so the bind takes an
			// int and reads anything other than nought as true.
			flag := C.int(0)
			if v {
				flag = 1
			}
			st = C.zu_bind_bool_z(stmt, cname, flag)
		default:
			cv := C.CString(fmt.Sprint(v))
			st = C.zu_bind_str_z(stmt, cname, cv)
			C.free(unsafe.Pointer(cv))
		}
		C.free(unsafe.Pointer(cname))
		// The bind calls carry no error handle: a NULL statement, a name
		// that is not UTF-8, and a closed connection are everything they
		// can report, so the status is the whole message.
		if st != C.ZU_OK {
			return fmt.Errorf("zu: bind %q failed with status %d", name, int(st))
		}
	}
	return nil
}

// decodeResult materializes a zu_result into the harness value model.
// Reads go column at a time: one call fetches a whole i64 or f64 column,
// so a wide result costs one boundary crossing per column instead of one
// per cell. Strings and the column kind are the exceptions, they are
// per-cell by construction.
func decodeResult(res *C.zu_result) (engine.Result, error) {
	rows := int(C.zu_result_rows(res))
	cols := int(C.zu_result_cols(res))

	names := make([]string, cols)
	out := make([][]engine.Value, rows)
	for i := range out {
		out[i] = make([]engine.Value, cols)
	}

	for c := 0; c < cols; c++ {
		var name *C.char
		var nameLen C.size_t
		if st := C.zu_result_col_name(res, C.uint32_t(c), &name, &nameLen); st != C.ZU_OK {
			return nil, fmt.Errorf("zu: column %d has no readable name, status %d", c, int(st))
		}
		names[c] = C.GoStringN(name, C.int(nameLen))
		if rows == 0 {
			continue
		}
		kind, err := columnKind(res, rows, c)
		if err != nil {
			return nil, err
		}
		switch kind {
		case C.ZU_TYPE_NULL:
			// Every cell is null; the rows are already nil.
		case C.ZU_TYPE_INT, C.ZU_TYPE_BOOL:
			var p *C.int64_t
			if st := C.zu_result_col_i64(res, C.uint32_t(c), &p); st != C.ZU_OK || p == nil {
				return nil, fmt.Errorf("zu: column %d is not readable as int64, status %d", c, int(st))
			}
			vals := unsafe.Slice((*int64)(unsafe.Pointer(p)), rows)
			valid := validSlice(res, rows, c)
			for r := 0; r < rows; r++ {
				if valid != nil && valid[r] == 0 {
					continue
				}
				if kind == C.ZU_TYPE_BOOL {
					out[r][c] = vals[r] != 0
				} else {
					out[r][c] = vals[r]
				}
			}
		case C.ZU_TYPE_NODE:
			// A node is not an integer to the C API: its identity comes
			// out of the offset accessor, and col_i64 refuses the column
			// rather than hand back a row number that looks like a value.
			var p *C.uint64_t
			if st := C.zu_result_col_node_offset(res, C.uint32_t(c), &p); st != C.ZU_OK || p == nil {
				return nil, fmt.Errorf("zu: column %d is not readable as a node offset, status %d", c, int(st))
			}
			vals := unsafe.Slice((*uint64)(unsafe.Pointer(p)), rows)
			valid := validSlice(res, rows, c)
			for r := 0; r < rows; r++ {
				if valid != nil && valid[r] == 0 {
					continue
				}
				out[r][c] = int64(vals[r])
			}
		case C.ZU_TYPE_FLOAT:
			var p *C.double
			if st := C.zu_result_col_f64(res, C.uint32_t(c), &p); st != C.ZU_OK || p == nil {
				return nil, fmt.Errorf("zu: column %d is not readable as float64, status %d", c, int(st))
			}
			vals := unsafe.Slice((*float64)(unsafe.Pointer(p)), rows)
			valid := validSlice(res, rows, c)
			for r := 0; r < rows; r++ {
				if valid != nil && valid[r] == 0 {
					continue
				}
				out[r][c] = vals[r]
			}
		case C.ZU_TYPE_STR:
			for r := 0; r < rows; r++ {
				var p *C.char
				var n C.size_t
				if st := C.zu_result_cell_str(res, C.uint64_t(r), C.uint32_t(c), &p, &n); st != C.ZU_OK || p == nil {
					continue // null cell, or a non-string cell in a string column
				}
				out[r][c] = C.GoStringN(p, C.int(n))
			}
		default:
			// Rel, list and path cells have no columnar read and no
			// canonical spelling here yet. Fail loudly: a silent
			// placeholder would pass verification against nothing.
			return nil, fmt.Errorf("zu: column %d holds cell type %d, which the adapter cannot decode yet", c, kind)
		}
	}
	return &result{cols: names, rows: out}, nil
}

// columnKind reports the type tag of the first non-null cell in a column,
// or ZU_TYPE_NULL when every cell is null. zu returns one type per
// column in practice, so this is read once and the column is decoded in
// bulk.
func columnKind(res *C.zu_result, rows, col int) (C.int32_t, error) {
	for r := 0; r < rows; r++ {
		var t C.int32_t
		if st := C.zu_result_cell_type(res, C.uint64_t(r), C.uint32_t(col), &t); st != C.ZU_OK {
			return C.ZU_TYPE_NULL, fmt.Errorf("zu: cell type of row %d column %d is unreadable, status %d", r, col, int(st))
		}
		if t != C.ZU_TYPE_NULL {
			return t, nil
		}
	}
	return C.ZU_TYPE_NULL, nil
}

// validSlice returns the column's validity bytes, or nil when the API
// declines to produce them (then every cell is treated as valid).
func validSlice(res *C.zu_result, rows, col int) []byte {
	var p *C.uint8_t
	if st := C.zu_result_col_valid(res, C.uint32_t(col), &p); st != C.ZU_OK || p == nil {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(p)), rows)
}

// takeErr turns a failed status and libzu's error handle into a Go error
// and releases the handle. fallback covers a failure that carried no
// message, which is every status the engine had nothing to add to.
func takeErr(st C.zu_status, cerr *C.zu_error, fallback string) error {
	if cerr == nil {
		return fmt.Errorf("%s (status %d)", fallback, int(st))
	}
	defer C.zu_error_free(cerr)
	var n C.size_t
	p := C.zu_error_message(cerr, &n)
	if p == nil {
		return fmt.Errorf("%s (status %d)", fallback, int(st))
	}
	return fmt.Errorf("zu: %s", C.GoStringN(p, C.int(n)))
}
