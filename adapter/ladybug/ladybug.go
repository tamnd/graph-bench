//go:build ladybug

// Package ladybug is the CGO adapter for the LadybugDB embedded graph
// database. LadybugDB is a Kuzu fork with a compatible C API. This adapter
// runs on the in-process plane: it opens a LadybugDB database in the harness
// process, bulk-loads a dataset via COPY FROM, and runs queries by calling the
// C API directly with no network hop.
//
// Build tag: ladybug. Use -tags ladybug to compile.
package ladybug

// #cgo CFLAGS: -I/opt/homebrew/include
// #cgo LDFLAGS: -L/opt/homebrew/lib -llbug
// #include "lbug.h"
// #include <stdlib.h>
import "C"

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/tamnd/graph-bench/target"
)

var errTxNotSupported = errors.New("ladybug: explicit transactions are not supported")

// Target is the LadybugDB in-process target.
type Target struct{}

// New returns a LadybugDB in-process target.
func New() *Target { return &Target{} }

var _ target.Target = (*Target)(nil)

func (t *Target) Name() string { return "ladybug" }

func (t *Target) Plane() target.Plane { return target.InProc }

func (t *Target) Capabilities() target.Capabilities {
	return target.Capabilities{
		Languages:      []target.Language{target.Cypher},
		Transactions:   false,
		BulkCSVLoad:    true,
		PersistentDisk: true,
	}
}

func (t *Target) Version(ctx context.Context) (string, error) {
	v := C.lbug_get_version()
	if v == nil {
		return "unknown", nil
	}
	return C.GoString(v), nil
}

// Setup opens a fresh LadybugDB database at cfg.Values["path"] and returns a
// Driver bound to it. An empty or ":memory:" path opens an in-memory database.
func (t *Target) Setup(ctx context.Context, cfg target.Config) (target.Driver, error) {
	path := cfg.Values["path"]
	if path == "" || path == ":memory:" {
		path = ":memory:"
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

	return &driver{db: db, conn: conn, path: path}, nil
}

// Load bulk-loads a dataset. File-backed datasets go through COPY FROM on temp
// CSVs with plain headers. Statement datasets run queries directly.
func (t *Target) Load(ctx context.Context, d target.Driver, ds target.Dataset) (target.LoadStats, error) {
	drv, ok := d.(*driver)
	if !ok {
		return target.LoadStats{}, fmt.Errorf("ladybug: Load got %T, want *driver", d)
	}

	if ds.Dir() == "" {
		return t.loadStatements(ctx, drv, ds)
	}
	return t.loadCSV(ctx, drv, ds)
}

func (t *Target) loadStatements(ctx context.Context, drv *driver, ds target.Dataset) (target.LoadStats, error) {
	start := time.Now()
	for i, stmt := range ds.Statements() {
		if err := drv.exec(stmt); err != nil {
			return target.LoadStats{}, fmt.Errorf("ladybug: load statement %d: %w", i, err)
		}
	}
	return target.LoadStats{Duration: time.Since(start), BytesOnDisk: -1}, nil
}

func (t *Target) loadCSV(ctx context.Context, drv *driver, ds target.Dataset) (target.LoadStats, error) {
	start := time.Now()
	schema := ds.Schema()

	// Sort labels and types so the load order is deterministic.
	labels := sortedNodeLabels(schema.Nodes)
	relTypes := sortedRelTypes(schema.Relationships)

	// Create all node tables first, then relationship tables.
	for _, label := range labels {
		files, cols, err := ds.NodeFiles(label)
		if err != nil {
			return target.LoadStats{}, fmt.Errorf("ladybug: node files %s: %w", label, err)
		}
		if len(files) == 0 {
			continue
		}
		ddl := buildNodeDDL(label, cols)
		if err := drv.exec(ddl); err != nil {
			return target.LoadStats{}, fmt.Errorf("ladybug: create node table %s: %w", label, err)
		}
		for _, f := range files {
			tmp, cleanup, err := writeStrippedNodeCSV(f, cols)
			if err != nil {
				return target.LoadStats{}, fmt.Errorf("ladybug: strip csv %s: %w", filepath.Base(f), err)
			}
			copyQ := fmt.Sprintf("COPY %s FROM '%s' (HEADER=true)", label, tmp)
			execErr := drv.exec(copyQ)
			cleanup()
			if execErr != nil {
				return target.LoadStats{}, fmt.Errorf("ladybug: COPY %s from %s: %w", label, filepath.Base(f), execErr)
			}
		}
	}

	for _, relType := range relTypes {
		rs := schema.Relationships[relType]
		files, cols, err := ds.RelFiles(relType)
		if err != nil {
			return target.LoadStats{}, fmt.Errorf("ladybug: rel files %s: %w", relType, err)
		}
		if len(files) == 0 {
			continue
		}
		ddl := buildRelDDL(relType, rs.Start, rs.End)
		if err := drv.exec(ddl); err != nil {
			return target.LoadStats{}, fmt.Errorf("ladybug: create rel table %s: %w", relType, err)
		}
		for _, f := range files {
			tmp, cleanup, err := writeStrippedRelCSV(f, cols)
			if err != nil {
				return target.LoadStats{}, fmt.Errorf("ladybug: strip csv %s: %w", filepath.Base(f), err)
			}
			copyQ := fmt.Sprintf("COPY %s FROM '%s' (HEADER=true)", relType, tmp)
			execErr := drv.exec(copyQ)
			cleanup()
			if execErr != nil {
				return target.LoadStats{}, fmt.Errorf("ladybug: COPY %s from %s: %w", relType, filepath.Base(f), execErr)
			}
		}
	}

	return target.LoadStats{Duration: time.Since(start), BytesOnDisk: -1}, nil
}

// Teardown closes the driver and its underlying database.
func (t *Target) Teardown(ctx context.Context, d target.Driver) error {
	if d == nil {
		return nil
	}
	return d.Close(ctx)
}

// driver is a live handle to an open LadybugDB database.
type driver struct {
	mu   sync.Mutex
	db   C.lbug_database
	conn C.lbug_connection
	path string
}

var _ target.Driver = (*driver)(nil)

// exec runs a DDL or DML statement and discards the result.
func (d *driver) exec(query string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	cq := C.CString(query)
	defer C.free(unsafe.Pointer(cq))
	var res C.lbug_query_result
	if state := C.lbug_connection_query(&d.conn, cq, &res); state != C.LbugSuccess {
		msg := C.lbug_query_result_get_error_message(&res)
		defer C.free(unsafe.Pointer(msg))
		C.lbug_query_result_destroy(&res)
		return fmt.Errorf("%s", C.GoString(msg))
	}
	if !C.lbug_query_result_is_success(&res) {
		msg := C.lbug_query_result_get_error_message(&res)
		defer C.free(unsafe.Pointer(msg))
		C.lbug_query_result_destroy(&res)
		return fmt.Errorf("%s", C.GoString(msg))
	}
	C.lbug_query_result_destroy(&res)
	return nil
}

// Run executes one query and returns a Result. If params is non-empty it uses a
// prepared statement so values are bound safely; otherwise it runs the query
// text directly.
func (d *driver) Run(ctx context.Context, q target.Query, params target.Params) (target.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(params) == 0 {
		return d.runDirect(q.Text)
	}
	return d.runPrepared(q.Text, params)
}

func (d *driver) runDirect(text string) (target.Result, error) {
	cq := C.CString(text)
	defer C.free(unsafe.Pointer(cq))
	var res C.lbug_query_result
	if state := C.lbug_connection_query(&d.conn, cq, &res); state != C.LbugSuccess {
		msg := C.lbug_query_result_get_error_message(&res)
		defer C.free(unsafe.Pointer(msg))
		C.lbug_query_result_destroy(&res)
		return nil, fmt.Errorf("ladybug: query: %s", C.GoString(msg))
	}
	if !C.lbug_query_result_is_success(&res) {
		msg := C.lbug_query_result_get_error_message(&res)
		defer C.free(unsafe.Pointer(msg))
		C.lbug_query_result_destroy(&res)
		return nil, fmt.Errorf("ladybug: query: %s", C.GoString(msg))
	}
	return newResult(res)
}

func (d *driver) runPrepared(text string, params target.Params) (target.Result, error) {
	cq := C.CString(text)
	defer C.free(unsafe.Pointer(cq))
	var stmt C.lbug_prepared_statement
	if state := C.lbug_connection_prepare(&d.conn, cq, &stmt); state != C.LbugSuccess {
		C.lbug_prepared_statement_destroy(&stmt)
		return nil, fmt.Errorf("ladybug: prepare failed")
	}
	if !C.lbug_prepared_statement_is_success(&stmt) {
		msg := C.lbug_prepared_statement_get_error_message(&stmt)
		defer C.free(unsafe.Pointer(msg))
		C.lbug_prepared_statement_destroy(&stmt)
		return nil, fmt.Errorf("ladybug: prepare: %s", C.GoString(msg))
	}
	for name, val := range params {
		cname := C.CString(name)
		if err := bindParam(&stmt, cname, val); err != nil {
			C.free(unsafe.Pointer(cname))
			C.lbug_prepared_statement_destroy(&stmt)
			return nil, fmt.Errorf("ladybug: bind %s: %w", name, err)
		}
		C.free(unsafe.Pointer(cname))
	}
	var res C.lbug_query_result
	if state := C.lbug_connection_execute(&d.conn, &stmt, &res); state != C.LbugSuccess {
		msg := C.lbug_query_result_get_error_message(&res)
		defer C.free(unsafe.Pointer(msg))
		C.lbug_prepared_statement_destroy(&stmt)
		C.lbug_query_result_destroy(&res)
		return nil, fmt.Errorf("ladybug: execute: %s", C.GoString(msg))
	}
	C.lbug_prepared_statement_destroy(&stmt)
	if !C.lbug_query_result_is_success(&res) {
		msg := C.lbug_query_result_get_error_message(&res)
		defer C.free(unsafe.Pointer(msg))
		C.lbug_query_result_destroy(&res)
		return nil, fmt.Errorf("ladybug: execute: %s", C.GoString(msg))
	}
	return newResult(res)
}

func bindParam(stmt *C.lbug_prepared_statement, cname *C.char, val target.Value) error {
	switch v := val.(type) {
	case int64:
		C.lbug_prepared_statement_bind_int64(stmt, cname, C.int64_t(v))
	case int:
		C.lbug_prepared_statement_bind_int64(stmt, cname, C.int64_t(v))
	case int32:
		C.lbug_prepared_statement_bind_int32(stmt, cname, C.int32_t(v))
	case float64:
		C.lbug_prepared_statement_bind_double(stmt, cname, C.double(v))
	case float32:
		C.lbug_prepared_statement_bind_float(stmt, cname, C.float(v))
	case bool:
		C.lbug_prepared_statement_bind_bool(stmt, cname, C.bool(v))
	case string:
		cs := C.CString(v)
		defer C.free(unsafe.Pointer(cs))
		C.lbug_prepared_statement_bind_string(stmt, cname, cs)
	default:
		cs := C.CString(fmt.Sprintf("%v", v))
		defer C.free(unsafe.Pointer(cs))
		C.lbug_prepared_statement_bind_string(stmt, cname, cs)
	}
	return nil
}

// Begin returns an error because LadybugDB has no explicit transaction API.
func (d *driver) Begin(ctx context.Context, mode target.AccessMode) (target.Tx, error) {
	return nil, errTxNotSupported
}

// Close destroys the connection and database handles.
func (d *driver) Close(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	C.lbug_connection_destroy(&d.conn)
	C.lbug_database_destroy(&d.db)
	return nil
}

// result wraps a lbug_query_result as a target.Result.
type result struct {
	qr      C.lbug_query_result
	cols    []string
	current []target.Value
	err     error
	done    bool
}

var _ target.Result = (*result)(nil)

func newResult(qr C.lbug_query_result) (*result, error) {
	n := int(C.lbug_query_result_get_num_columns(&qr))
	cols := make([]string, n)
	for i := 0; i < n; i++ {
		var cname *C.char
		C.lbug_query_result_get_column_name(&qr, C.uint64_t(i), &cname)
		if cname != nil {
			cols[i] = C.GoString(cname)
			C.free(unsafe.Pointer(cname))
		}
	}
	return &result{qr: qr, cols: cols}, nil
}

func (r *result) Columns() []string { return r.cols }

func (r *result) Next() bool {
	if r.done || r.err != nil {
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
	// Copy all values from the tuple now; the tuple memory is reused on the
	// next call to get_next.
	row := make([]target.Value, len(r.cols))
	for i := range r.cols {
		var val C.lbug_value
		if state := C.lbug_flat_tuple_get_value(&tuple, C.uint64_t(i), &val); state != C.LbugSuccess {
			row[i] = nil
			continue
		}
		row[i] = extractValue(&val)
	}
	r.current = row
	return true
}

func (r *result) Row() []target.Value { return r.current }

func (r *result) Err() error { return r.err }

func (r *result) Close() error {
	C.lbug_query_result_destroy(&r.qr)
	return nil
}

// extractValue reads a C lbug_value into a Go target.Value. The value pointer
// is valid for the duration of this call only (tuple memory is reused).
func extractValue(val *C.lbug_value) target.Value {
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
	case C.LBUG_INT64, C.LBUG_UINT64:
		var out C.int64_t
		C.lbug_value_get_int64(val, &out)
		return int64(out)
	case C.LBUG_DOUBLE, C.LBUG_FLOAT:
		var out C.double
		C.lbug_value_get_double(val, &out)
		return float64(out)
	case C.LBUG_STRING:
		var out *C.char
		C.lbug_value_get_string(val, &out)
		if out == nil {
			return ""
		}
		s := C.GoString(out)
		C.free(unsafe.Pointer(out))
		return s
	default:
		// Fall through to string representation for nodes, rels, and other types.
		out := C.lbug_value_to_string(val)
		if out == nil {
			return nil
		}
		s := C.GoString(out)
		C.free(unsafe.Pointer(out))
		return s
	}
}

// buildNodeDDL returns the CREATE NODE TABLE statement for a label.
// id column becomes INT64 PRIMARY KEY; other property columns map to their types.
func buildNodeDDL(label string, cols []target.Column) string {
	idName := "id"
	for _, c := range cols {
		if c.Type == "ID" {
			if c.Name != "" {
				idName = c.Name
			}
			break
		}
	}
	var props []string
	props = append(props, idName+" INT64")
	for _, c := range cols {
		if c.Type == "ID" || isStructural(c.Type) {
			continue
		}
		props = append(props, c.Name+" "+mapType(c.Type))
	}
	props = append(props, "PRIMARY KEY("+idName+")")
	return fmt.Sprintf("CREATE NODE TABLE %s (%s)", label, strings.Join(props, ", "))
}

// buildRelDDL returns the CREATE REL TABLE statement for a relationship type.
// LadybugDB rel tables do not carry property definitions in the CREATE statement
// when loaded via COPY FROM; the file provides the values.
func buildRelDDL(relType, fromLabel, toLabel string) string {
	return fmt.Sprintf("CREATE REL TABLE %s (FROM %s TO %s)", relType, fromLabel, toLabel)
}

func mapType(t string) string {
	switch strings.ToUpper(t) {
	case "INT64", "LONG":
		return "INT64"
	case "INT32", "INT", "INTEGER":
		return "INT32"
	case "FLOAT", "FLOAT32":
		return "FLOAT"
	case "DOUBLE", "FLOAT64":
		return "DOUBLE"
	case "BOOL", "BOOLEAN":
		return "BOOLEAN"
	case "ID", "START_ID", "END_ID":
		return "INT64"
	default:
		return "STRING"
	}
}

func isStructural(t string) bool {
	switch t {
	case "ID", "LABEL", "TYPE", "START_ID", "END_ID":
		return true
	}
	return false
}

// writeStrippedNodeCSV copies a node CSV file to a temp file with plain
// headers (no :TYPE annotations). The :ID column becomes the primary key
// column; structural columns other than :ID are dropped. Returns the temp
// file path and a cleanup func that deletes it.
func writeStrippedNodeCSV(src string, cols []target.Column) (string, func(), error) {
	f, err := os.Open(src)
	if err != nil {
		return "", func() {}, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil {
		return "", func() {}, fmt.Errorf("read csv: %w", err)
	}
	if len(rows) == 0 {
		return "", func() {}, fmt.Errorf("empty csv")
	}

	// Find which column indices to keep and what to name them.
	type colInfo struct {
		idx  int
		name string
	}
	var kept []colInfo
	for i, c := range cols {
		if c.Type == "LABEL" || c.Type == "TYPE" {
			continue
		}
		name := c.Name
		if c.Type == "ID" {
			if name == "" {
				name = "id"
			}
			// ID goes first.
			kept = append([]colInfo{{i, name}}, kept...)
			continue
		}
		kept = append(kept, colInfo{i, name})
	}

	tmp, err := os.CreateTemp("", "lbug-node-*.csv")
	if err != nil {
		return "", func() {}, fmt.Errorf("temp file: %w", err)
	}
	w := csv.NewWriter(tmp)

	header := make([]string, len(kept))
	for i, k := range kept {
		header[i] = k.name
	}
	if err := w.Write(header); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", func() {}, err
	}

	// Skip the original header row.
	for _, row := range rows[1:] {
		out := make([]string, len(kept))
		for i, k := range kept {
			if k.idx < len(row) {
				out[i] = row[k.idx]
			}
		}
		if err := w.Write(out); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return "", func() {}, err
		}
	}
	w.Flush()
	tmp.Close()
	if err := w.Error(); err != nil {
		os.Remove(tmp.Name())
		return "", func() {}, err
	}

	name := tmp.Name()
	return name, func() { os.Remove(name) }, nil
}

// writeStrippedRelCSV copies a rel CSV file to a temp file with plain
// headers. :START_ID becomes "from", :END_ID becomes "to"; :TYPE is dropped.
func writeStrippedRelCSV(src string, cols []target.Column) (string, func(), error) {
	f, err := os.Open(src)
	if err != nil {
		return "", func() {}, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	rows, err := r.ReadAll()
	if err != nil {
		return "", func() {}, fmt.Errorf("read csv: %w", err)
	}
	if len(rows) == 0 {
		return "", func() {}, fmt.Errorf("empty csv")
	}

	type colInfo struct {
		idx  int
		name string
	}
	// from and to must be first two columns for LadybugDB COPY FROM on rel tables.
	var fromIdx, toIdx int = -1, -1
	var props []colInfo
	for i, c := range cols {
		switch c.Type {
		case "START_ID":
			fromIdx = i
		case "END_ID":
			toIdx = i
		case "TYPE", "LABEL":
			// skip
		default:
			props = append(props, colInfo{i, c.Name})
		}
	}

	tmp, err := os.CreateTemp("", "lbug-rel-*.csv")
	if err != nil {
		return "", func() {}, fmt.Errorf("temp file: %w", err)
	}
	w := csv.NewWriter(tmp)

	header := []string{"from", "to"}
	for _, p := range props {
		header = append(header, p.name)
	}
	if err := w.Write(header); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", func() {}, err
	}

	for _, row := range rows[1:] {
		out := make([]string, 2+len(props))
		if fromIdx >= 0 && fromIdx < len(row) {
			out[0] = row[fromIdx]
		}
		if toIdx >= 0 && toIdx < len(row) {
			out[1] = row[toIdx]
		}
		for i, p := range props {
			if p.idx < len(row) {
				out[2+i] = row[p.idx]
			}
		}
		if err := w.Write(out); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return "", func() {}, err
		}
	}
	w.Flush()
	tmp.Close()
	if err := w.Error(); err != nil {
		os.Remove(tmp.Name())
		return "", func() {}, err
	}

	name := tmp.Name()
	return name, func() { os.Remove(name) }, nil
}

func sortedNodeLabels(m map[string]target.NodeSchema) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func sortedRelTypes(m map[string]target.RelSchema) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
