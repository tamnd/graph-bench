package sqlbase

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// result streams *sql.Rows into the canonical value model. The rows are
// not materialized: the runner drains a result before it stops the clock,
// so streaming keeps the decode inside the measured region where it
// belongs rather than paying for it in a burst before the first Next.
type result struct {
	rows *sql.Rows
	cols []string
	conv []conversion
	dest []any
	row  []engine.Value
	err  error
}

var _ engine.Result = (*result)(nil)

// conversion is what a column annotation asks for after the driver has
// decoded the value.
type conversion int

const (
	convNone conversion = iota
	convBool
)

func newResult(rows *sql.Rows) (engine.Result, error) {
	raw, err := rows.Columns()
	if err != nil {
		rows.Close()
		return nil, fmt.Errorf("columns: %w", err)
	}
	r := &result{
		rows: rows,
		cols: make([]string, len(raw)),
		conv: make([]conversion, len(raw)),
		dest: make([]any, len(raw)),
		row:  make([]engine.Value, len(raw)),
	}
	for i, name := range raw {
		r.cols[i], r.conv[i], err = splitAnnotation(name)
		if err != nil {
			rows.Close()
			return nil, err
		}
		r.dest[i] = new(any)
	}
	return r, nil
}

// splitAnnotation reads a "name::type" alias. An alias with no :: is a
// plain column name.
func splitAnnotation(alias string) (string, conversion, error) {
	name, typ, found := strings.Cut(alias, "::")
	if !found {
		return alias, convNone, nil
	}
	switch typ {
	case "bool":
		return name, convBool, nil
	default:
		return "", convNone, fmt.Errorf("column %q: %q is not a type this dialect annotates", alias, typ)
	}
}

func (r *result) Columns() []string { return r.cols }

func (r *result) Next() bool {
	if r.err != nil || !r.rows.Next() {
		return false
	}
	if err := r.rows.Scan(r.dest...); err != nil {
		r.err = fmt.Errorf("scan: %w", err)
		return false
	}
	for i := range r.dest {
		v, err := decode(*(r.dest[i].(*any)), r.conv[i])
		if err != nil {
			r.err = fmt.Errorf("column %q: %w", r.cols[i], err)
			return false
		}
		r.row[i] = v
	}
	return true
}

func (r *result) Row() []engine.Value { return r.row }

func (r *result) Err() error {
	if r.err != nil {
		return r.err
	}
	return r.rows.Err()
}

func (r *result) Close() error { return r.rows.Close() }

// decode normalizes a driver value into the canonical model: integer
// widths to int64, floats to float64, text and bytes to string, and
// whatever the annotation asked for on top.
func decode(v any, conv conversion) (engine.Value, error) {
	var out engine.Value
	switch t := v.(type) {
	case nil:
		out = nil
	case bool:
		out = t
	case int:
		out = int64(t)
	case int32:
		out = int64(t)
	case int64:
		out = t
	case float32:
		out = float64(t)
	case float64:
		out = t
	case []byte:
		out = string(t)
	case string:
		out = t
	case time.Time:
		out = t
	default:
		return nil, fmt.Errorf("driver returned %T, which has no canonical form here", v)
	}
	if conv != convBool {
		return out, nil
	}
	switch t := out.(type) {
	case nil:
		return nil, nil
	case bool:
		return t, nil
	case int64:
		return t != 0, nil
	default:
		return nil, fmt.Errorf("annotated ::bool but the value is %T", out)
	}
}
