//go:build zuinproc

// In-process zu adapter, over libzu (crates/zu-capi). Same engine as the
// Subprocess adapter in this package, different plane: the database opens
// inside the harness process and every query is a direct call, with no
// frame, no pipe, and no child process in the timed region. That is the
// plane ladybug runs on, so this is the adapter that makes the two
// comparable.
//
// Build tag: zuinproc (spec 01 §3.2). Registered under the name "zu-capi"
// so it can sit in
// the same table as "zu" and show what the subprocess frame costs.
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
// The header states a session is not thread safe. Exec is mutex
// serialized and Capabilities.MaxConcurrency stays 1, so calls never
// overlap. zu's session holds no thread-local state, so serialized calls
// from different goroutines are fine.
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
	"os/exec"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"github.com/tamnd/graph-bench/engine"
)

// InprocEngine is the libzu engine descriptor. Zero value is ready to use.
type InprocEngine struct{}

// NewInproc returns the in-process zu engine descriptor. Nothing opens
// until Start.
func NewInproc() *InprocEngine { return &InprocEngine{} }

var _ engine.Engine = (*InprocEngine)(nil)

// Info reports the same engine and the same honest capabilities as the
// subprocess adapter, on the in-process plane. MaxConcurrency stays 1
// because a libzu session is not thread safe.
//
// The dialect chain stays zuQL only, where spec 01 §3.2 asks for
// "zuql, cypher". Coverage is a property of the engine, not of the plane
// it is reached over, and the reason the subprocess adapter gives holds
// here word for word: a query with no zuQL text would FAIL on the Cypher
// text instead of SKIPping, and one FAIL discards the measurement for
// every query in the workload. The chain widens when zu's parser earns
// it, on both adapters at once.
func (e *InprocEngine) Info() engine.Info {
	info := (&Engine{}).Info()
	info.Name = "zu-capi"
	info.Plane = engine.InProc
	return info
}

// Start discovers the zu binary (Load needs the copy verb) and binds a
// session to a database file: Config["path"] if given, else "bench.zu1"
// in a fresh temp dir, removed on Close. The libzu session opens lazily,
// on the first query after the file exists.
func (e *InprocEngine) Start(ctx context.Context, cfg engine.Config) (engine.Session, error) {
	bin, err := discoverBinary(cfg)
	if err != nil {
		return nil, err
	}
	workDir, err := os.MkdirTemp("", "graph-bench-zu-capi-")
	if err != nil {
		return nil, fmt.Errorf("zu-capi: create work dir: %w", err)
	}
	dbPath := cfg.Get("path", "")
	if dbPath == "" {
		dbPath = filepath.Join(workDir, "bench.zu1")
	}
	return &inprocSession{bin: bin, dbPath: dbPath, workDir: workDir}, nil
}

// inprocSession is one open libzu session plus its prepared-statement
// cache, keyed by query text the way a real embedding host would keep it.
type inprocSession struct {
	bin     string
	dbPath  string
	workDir string

	mu     sync.Mutex
	sess   *C.zu_session
	stmts  map[string]*C.zu_stmt
	closed bool
}

var _ engine.Session = (*inprocSession)(nil)

// Bin reports the discovered binary path, for the condition stamp.
func (s *inprocSession) Bin() string { return s.bin }

// Mode reports the exec surface, for the condition stamp. There is only
// one here: direct calls into libzu.
func (s *inprocSession) Mode() string { return "capi" }

// Version reports the library version from zu_version(), which is the
// version of the code actually linked in, not of a binary on PATH.
func (s *inprocSession) Version(ctx context.Context) (string, error) {
	return C.GoString(C.zu_version()), nil
}

// Begin always fails: zu has no transactions.
func (s *inprocSession) Begin(ctx context.Context, mode engine.AccessMode) (engine.Tx, error) {
	return nil, engine.ErrNoTransactions
}

// Calibrate reports the per-operation process-spawn floor, which is zero
// here: nothing spawns per operation. The number exists so a run on this
// plane can be told apart from a subprocess run in the stamped condition.
func (s *inprocSession) Calibrate(ctx context.Context) time.Duration { return 0 }

// Load bulk-loads through the CLI, the same edge list and the same
// `zu copy --reorder degree` the subprocess adapter uses, then drops any
// open session so the next query opens the file that copy just wrote.
// libzu has no load entry point; when it grows one this moves in-process
// too. Load is outside every timed region either way.
func (s *inprocSession) Load(ctx context.Context, ds engine.Dataset) (engine.LoadStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return engine.LoadStats{}, errors.New("zu-capi: session is closed")
	}
	if ds.Dir() == "" {
		return engine.LoadStats{}, fmt.Errorf(
			"zu-capi: dataset %q is statements-only; libzu has no write surface yet, run it on the zu subprocess adapter", ds.Name())
	}

	edgesPath := filepath.Join(s.workDir, "edges.txt")
	counted, err := materializeEdges(ds, edgesPath)
	if err != nil {
		return engine.LoadStats{}, err
	}
	start := time.Now()
	out, err := exec.CommandContext(ctx, s.bin,
		"copy", "--reorder", "degree", edgesPath, s.dbPath).CombinedOutput()
	if err != nil {
		return engine.LoadStats{}, fmt.Errorf("zu-capi: copy failed: %v\n%s", err, out)
	}
	s.closeSessionLocked()
	return parseCopyStats(string(out), s.dbPath, time.Since(start), counted), nil
}

// Exec runs one operation as a prepared statement: compile on the first
// sighting of the text, bind the parameters, execute. Mutex serialized
// (MaxConcurrency 1). libzu has no cancellation, so the context is
// checked on the way in and not again until the call returns.
func (s *inprocSession) Exec(ctx context.Context, op engine.Op) (engine.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("zu-capi: session is closed")
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

	var cerr *C.char
	res := C.zu_execute(stmt, &cerr)
	if res == nil {
		return nil, takeErr(cerr, "zu-capi: execute failed")
	}
	defer C.zu_result_free(res)
	return decodeResult(res)
}

// Close frees the prepared statements, closes the session, and removes
// the work dir. Statements go first: the header requires it. Idempotent.
func (s *inprocSession) Close(ctx context.Context) error {
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
func (s *inprocSession) openLocked() error {
	if s.sess != nil {
		return nil
	}
	cpath := C.CString(s.dbPath)
	defer C.free(unsafe.Pointer(cpath))
	var cerr *C.char
	sess := C.zu_open(cpath, &cerr)
	if sess == nil {
		return takeErr(cerr, fmt.Sprintf("zu-capi: open %q failed", s.dbPath))
	}
	s.sess = sess
	return nil
}

// closeSessionLocked tears down statements then the session, leaving the
// struct ready to reopen. Caller holds s.mu.
func (s *inprocSession) closeSessionLocked() {
	for _, stmt := range s.stmts {
		C.zu_stmt_close(stmt)
	}
	s.stmts = nil
	if s.sess != nil {
		C.zu_close(s.sess)
		s.sess = nil
	}
}

// preparedLocked returns the cached statement for text, compiling it on
// first sighting. Caller holds s.mu.
func (s *inprocSession) preparedLocked(text string) (*C.zu_stmt, error) {
	if stmt, ok := s.stmts[text]; ok {
		return stmt, nil
	}
	ctext := C.CString(text)
	defer C.free(unsafe.Pointer(ctext))
	var cerr *C.char
	stmt := C.zu_prepare(s.sess, ctext, &cerr)
	if stmt == nil {
		return nil, takeErr(cerr, "zu-capi: prepare failed")
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
		switch v := val.(type) {
		case nil:
			C.zu_bind_null(stmt, cname)
		case int:
			C.zu_bind_i64(stmt, cname, C.int64_t(v))
		case int32:
			C.zu_bind_i64(stmt, cname, C.int64_t(v))
		case int64:
			C.zu_bind_i64(stmt, cname, C.int64_t(v))
		case float32:
			C.zu_bind_f64(stmt, cname, C.double(v))
		case float64:
			C.zu_bind_f64(stmt, cname, C.double(v))
		case string:
			cv := C.CString(v)
			C.zu_bind_str(stmt, cname, cv)
			C.free(unsafe.Pointer(cv))
		case bool:
			// zuQL has no boolean literal or boolean bind, the same gap
			// the subprocess adapter reports. Fail rather than smuggle
			// one in as 0/1.
			C.free(unsafe.Pointer(cname))
			return fmt.Errorf("zu-capi: parameter %q is a bool; zu has no boolean parameters", name)
		default:
			cv := C.CString(fmt.Sprint(v))
			C.zu_bind_str(stmt, cname, cv)
			C.free(unsafe.Pointer(cv))
		}
		C.free(unsafe.Pointer(cname))
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
		names[c] = C.GoString(C.zu_result_col_name(res, C.uint32_t(c)))
		if rows == 0 {
			continue
		}
		kind := columnKind(res, rows, c)
		switch kind {
		case C.ZU_TYPE_NULL:
			// Every cell is null; the rows are already nil.
		case C.ZU_TYPE_INT, C.ZU_TYPE_BOOL, C.ZU_TYPE_NODE:
			p := C.zu_result_col_i64(res, C.uint32_t(c))
			if p == nil {
				return nil, fmt.Errorf("zu-capi: column %d is not readable as int64", c)
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
		case C.ZU_TYPE_FLOAT:
			p := C.zu_result_col_f64(res, C.uint32_t(c))
			if p == nil {
				return nil, fmt.Errorf("zu-capi: column %d is not readable as float64", c)
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
				var n C.size_t
				p := C.zu_result_cell_str(res, C.uint64_t(r), C.uint32_t(c), &n)
				if p == nil {
					continue // null cell, or a non-string cell in a string column
				}
				out[r][c] = C.GoStringN(p, C.int(n))
			}
		default:
			// Rel, list and path cells have no columnar read and no
			// canonical spelling here yet. Fail loudly: a silent
			// placeholder would pass verification against nothing.
			return nil, fmt.Errorf("zu-capi: column %d holds cell type %d, which the adapter cannot decode yet", c, kind)
		}
	}
	return &result{cols: names, rows: out}, nil
}

// columnKind reports the type tag of the first non-null cell in a column,
// or ZU_TYPE_NULL when every cell is null. zu returns one type per
// column in practice, so this is read once and the column is decoded in
// bulk.
func columnKind(res *C.zu_result, rows, col int) C.int32_t {
	for r := 0; r < rows; r++ {
		t := C.zu_result_cell_type(res, C.uint64_t(r), C.uint32_t(col))
		if t != C.ZU_TYPE_NULL {
			return t
		}
	}
	return C.ZU_TYPE_NULL
}

// validSlice returns the column's validity bytes, or nil when the API
// declines to produce them (then every cell is treated as valid).
func validSlice(res *C.zu_result, rows, col int) []byte {
	p := C.zu_result_col_valid(res, C.uint32_t(col))
	if p == nil {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(p)), rows)
}

// takeErr turns libzu's char** error out-parameter into a Go error and
// frees it. fallback covers a failure that set no message.
func takeErr(cerr *C.char, fallback string) error {
	if cerr == nil {
		return errors.New(fallback)
	}
	msg := C.GoString(cerr)
	C.zu_string_free(cerr)
	return fmt.Errorf("zu-capi: %s", msg)
}
