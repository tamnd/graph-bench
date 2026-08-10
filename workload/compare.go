package workload

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// This file is the normalized comparison (spec 08 §5): the single routine that
// judges an engine's answer against the reference under the value model. It is
// what turns "fast" into "fast and correct": a query is timed only after its
// answer matches here. The comparison normalizes away the differences that are
// engine choices rather than wrong answers (row order when the query does not
// fix it, the int-versus-float spelling of a count, the engine-specific element
// id of a returned node) and holds the line on the differences that are real (a
// missing row, a wrong property, a different count).

// FloatTolerance returns the effective relative float tolerance, applying the
// 1e-9 default (spec 08 §5) when the spec leaves it zero.
func (c CompareSpec) FloatTolerance() float64 {
	if c.FloatTol == 0 {
		return 1e-9
	}
	return c.FloatTol
}

// Compare reports whether an engine's answer matches the reference under the
// CompareSpec, returning nil on a match and an error naming the first
// divergence on a mismatch. The error is the mismatch record the validator
// attaches to the result, so it names what differed in terms a reader can act
// on, not a bare "false".
//
// The answers' own Unordered fields are advisory carriers from the value
// model; the authority is the CompareSpec the query author set, so the spec's
// Ordered and FloatTol win. A nil reference is a programming error (a query
// reached validation with no reference) and is reported as one.
func Compare(got, want *Answer, spec CompareSpec) error {
	if want == nil {
		return fmt.Errorf("compare: nil reference answer")
	}
	if got == nil {
		return fmt.Errorf("compare: nil engine answer")
	}
	if !sameColumns(got.Columns, want.Columns) {
		return fmt.Errorf("compare: columns differ: got %v, want %v", got.Columns, want.Columns)
	}
	if len(got.Rows) != len(want.Rows) {
		return fmt.Errorf("compare: row count differs: got %d, want %d", len(got.Rows), len(want.Rows))
	}

	gotRows, wantRows := got.Rows, want.Rows
	if !spec.Ordered {
		// Unordered: sort both sides by a canonical key so two engines that
		// emit the same set in different orders both validate. The key is
		// built under the same normalization as the comparison (ids excluded
		// unless the spec includes them), so the sort cannot separate two
		// rows the comparison would call equal.
		gotRows = sortedRows(gotRows, spec)
		wantRows = sortedRows(wantRows, spec)
	}

	tol := spec.FloatTolerance()
	for i := range wantRows {
		gr, wr := gotRows[i], wantRows[i]
		if len(gr) != len(wr) {
			return fmt.Errorf("compare: row %d has %d columns, want %d", i, len(gr), len(wr))
		}
		for j := range wr {
			if err := valueEqual(gr[j], wr[j], spec, tol); err != nil {
				return fmt.Errorf("compare: row %d column %q: %w", i, columnName(want.Columns, j), err)
			}
		}
	}
	return nil
}

// valueEqual reports whether an engine value matches a reference value under
// the spec, returning nil on a match and a describing error on a mismatch. It
// walks the value model recursively: scalars by type and value (with float
// tolerance and optional numeric coercion), lists element-wise in order, maps
// key-wise, and the graph objects by their logical content (labels, type,
// properties) with the element id excluded unless the spec includes it.
func valueEqual(got, want engine.Value, spec CompareSpec, tol float64) error {
	// Numbers first, so coercion and tolerance apply before the strict type
	// switch would reject an int-versus-float spelling of the same count.
	if gf, gok := asFloat(got); gok {
		if wf, wok := asFloat(want); wok {
			return floatEqual(got, want, gf, wf, spec, tol)
		}
		return fmt.Errorf("got number %v, want %T %v", got, want, want)
	}
	// A numeric reference against a non-numeric engine value is a plain type
	// mismatch. Without this the switch below falls through to its default and
	// blames the reference's type as unsupported, which sends the reader to
	// audit the value model instead of the answer.
	if _, wok := asFloat(want); wok {
		return fmt.Errorf("got %T %v, want number %v", got, got, want)
	}

	switch w := want.(type) {
	case nil:
		if got != nil {
			return fmt.Errorf("got %v, want null", got)
		}
		return nil
	case bool:
		g, ok := got.(bool)
		if !ok || g != w {
			return fmt.Errorf("got %v, want bool %v", got, w)
		}
		return nil
	case string:
		g, ok := got.(string)
		if !ok || g != w {
			return fmt.Errorf("got %v, want string %q", got, w)
		}
		return nil
	case time.Time:
		g, ok := got.(time.Time)
		if !ok || !g.Equal(w) {
			return fmt.Errorf("got %v, want time %v", got, w)
		}
		return nil
	case []byte:
		g, ok := got.([]byte)
		if !ok || string(g) != string(w) {
			return fmt.Errorf("got %v, want bytes %q", got, w)
		}
		return nil
	case []engine.Value:
		return listEqual(got, w, spec, tol)
	case map[string]engine.Value:
		return mapEqual(got, w, spec, tol)
	case engine.Node:
		return nodeEqual(got, w, spec, tol)
	case engine.Rel:
		return relEqual(got, w, spec, tol)
	case engine.Path:
		return pathEqual(got, w, spec, tol)
	default:
		return fmt.Errorf("compare: unsupported reference value type %T", want)
	}
}

// floatEqual compares two numeric values. When neither side is coerced (both
// are the same kind, integer or float) it compares exactly for integers and
// within tolerance for floats; CoerceNum lets an integer reference match a
// float engine value (the count-as-float case). Without CoerceNum an integer
// reference and a float engine value are a mismatch, because a query that
// should return an integer returning a float is a real difference the author
// opted out of only by setting the flag.
func floatEqual(got, want engine.Value, gf, wf float64, spec CompareSpec, tol float64) error {
	gInt := isInteger(got)
	wInt := isInteger(want)
	if gInt != wInt && !spec.CoerceNum {
		return fmt.Errorf("got %v (%T), want %v (%T): numeric kinds differ and CoerceNum is off", got, got, want, want)
	}
	if gInt && wInt {
		if gf != wf {
			return fmt.Errorf("got %v, want %v", got, want)
		}
		return nil
	}
	if !withinTol(gf, wf, tol) {
		return fmt.Errorf("got %v, want %v (rel tol %g)", got, want, tol)
	}
	return nil
}

// listEqual compares two lists element-wise in order. List order is part of
// the value, so it is compared even in an unordered query: an unordered query
// sorts rows, not the lists within a row.
func listEqual(got engine.Value, want []engine.Value, spec CompareSpec, tol float64) error {
	g, ok := got.([]engine.Value)
	if !ok {
		return fmt.Errorf("got %T, want list", got)
	}
	if len(g) != len(want) {
		return fmt.Errorf("list length: got %d, want %d", len(g), len(want))
	}
	for i := range want {
		if err := valueEqual(g[i], want[i], spec, tol); err != nil {
			return fmt.Errorf("list[%d]: %w", i, err)
		}
	}
	return nil
}

// mapEqual compares two maps key-wise: the same key set and an equal value per
// key. Map iteration order does not matter.
func mapEqual(got engine.Value, want map[string]engine.Value, spec CompareSpec, tol float64) error {
	g, ok := got.(map[string]engine.Value)
	if !ok {
		return fmt.Errorf("got %T, want map", got)
	}
	if len(g) != len(want) {
		return fmt.Errorf("map size: got %d keys, want %d", len(g), len(want))
	}
	for k, wv := range want {
		gv, ok := g[k]
		if !ok {
			return fmt.Errorf("map missing key %q", k)
		}
		if err := valueEqual(gv, wv, spec, tol); err != nil {
			return fmt.Errorf("map[%q]: %w", k, err)
		}
	}
	return nil
}

// nodeEqual compares two nodes by their logical content: the label set (order
// independent) and the properties. The element id is compared only when the
// spec includes it, because two engines assign different ids to the same
// logical node.
func nodeEqual(got engine.Value, want engine.Node, spec CompareSpec, tol float64) error {
	g, ok := got.(engine.Node)
	if !ok {
		return fmt.Errorf("got %T, want node", got)
	}
	if spec.IncludeElementIDs && g.ID != want.ID {
		return fmt.Errorf("node id: got %q, want %q", g.ID, want.ID)
	}
	if !sameSet(g.Labels, want.Labels) {
		return fmt.Errorf("node labels: got %v, want %v", g.Labels, want.Labels)
	}
	if err := mapEqual(g.Props, want.Props, spec, tol); err != nil {
		return fmt.Errorf("node props: %w", err)
	}
	return nil
}

// relEqual compares two relationships by type and properties, and by
// endpoints only when the spec includes element ids (the endpoint ids are
// themselves engine-specific element ids).
func relEqual(got engine.Value, want engine.Rel, spec CompareSpec, tol float64) error {
	g, ok := got.(engine.Rel)
	if !ok {
		return fmt.Errorf("got %T, want relationship", got)
	}
	if g.Type != want.Type {
		return fmt.Errorf("rel type: got %q, want %q", g.Type, want.Type)
	}
	if spec.IncludeElementIDs {
		if g.ID != want.ID {
			return fmt.Errorf("rel id: got %q, want %q", g.ID, want.ID)
		}
		if g.Start != want.Start || g.End != want.End {
			return fmt.Errorf("rel endpoints: got %q->%q, want %q->%q", g.Start, g.End, want.Start, want.End)
		}
	}
	if err := mapEqual(g.Props, want.Props, spec, tol); err != nil {
		return fmt.Errorf("rel props: %w", err)
	}
	return nil
}

// pathEqual compares two paths node-for-node and relationship-for-relationship
// in traversal order, deferring to nodeEqual and relEqual for each element.
func pathEqual(got engine.Value, want engine.Path, spec CompareSpec, tol float64) error {
	g, ok := got.(engine.Path)
	if !ok {
		return fmt.Errorf("got %T, want path", got)
	}
	if len(g.Nodes) != len(want.Nodes) || len(g.Rels) != len(want.Rels) {
		return fmt.Errorf("path shape: got %d nodes/%d rels, want %d nodes/%d rels", len(g.Nodes), len(g.Rels), len(want.Nodes), len(want.Rels))
	}
	for i := range want.Nodes {
		if err := nodeEqual(g.Nodes[i], want.Nodes[i], spec, tol); err != nil {
			return fmt.Errorf("path node %d: %w", i, err)
		}
	}
	for i := range want.Rels {
		if err := relEqual(g.Rels[i], want.Rels[i], spec, tol); err != nil {
			return fmt.Errorf("path rel %d: %w", i, err)
		}
	}
	return nil
}

// sameColumns reports whether two column-name slices are equal in order.
// Column order is part of the answer shape and is always compared, ordered or
// not.
func sameColumns(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sameSet reports whether two string slices hold the same multiset of values,
// order independent. Used for label sets.
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ca := append([]string(nil), a...)
	cb := append([]string(nil), b...)
	sort.Strings(ca)
	sort.Strings(cb)
	for i := range ca {
		if ca[i] != cb[i] {
			return false
		}
	}
	return true
}

// columnName returns the name of column j for an error message, falling back
// to the index when the column list is short.
func columnName(cols []string, j int) string {
	if j >= 0 && j < len(cols) {
		return cols[j]
	}
	return fmt.Sprintf("#%d", j)
}

// sortedRows returns a copy of rows sorted by a canonical key, for the
// unordered comparison. The key is a string rendering of the row under the
// same id-exclusion the comparison uses, so rows the comparison would call
// equal sort adjacent and the pairing in Compare lines matching rows up.
func sortedRows(rows [][]engine.Value, spec CompareSpec) [][]engine.Value {
	out := make([][]engine.Value, len(rows))
	copy(out, rows)
	sort.SliceStable(out, func(i, j int) bool {
		return rowKey(out[i], spec) < rowKey(out[j], spec)
	})
	return out
}

// rowKey renders a row to a canonical string key for the unordered sort. It is
// a total order over rows that agrees with valueEqual on equality up to float
// formatting; ties (two rows with the same key) are fine because the
// subsequent element-wise comparison is the authority.
func rowKey(row []engine.Value, spec CompareSpec) string {
	var b strings.Builder
	for i, v := range row {
		if i > 0 {
			b.WriteByte('\x1f')
		}
		writeKey(&b, v, spec)
	}
	return b.String()
}

// writeKey appends a canonical rendering of one value to the key builder,
// mirroring the structure valueEqual walks so the sort key cannot disagree
// with the comparison about which rows are equal.
func writeKey(b *strings.Builder, v engine.Value, spec CompareSpec) {
	if f, ok := asFloat(v); ok {
		// One numeric spelling for both int and float so a coerced pair keys
		// the same. The precision is well beyond the default tolerance.
		fmt.Fprintf(b, "n:%.12g", f)
		return
	}
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		fmt.Fprintf(b, "b:%t", x)
	case string:
		b.WriteString("s:")
		b.WriteString(x)
	case time.Time:
		b.WriteString("t:")
		b.WriteString(x.UTC().Format(time.RFC3339Nano))
	case []byte:
		b.WriteString("y:")
		b.Write(x)
	case []engine.Value:
		b.WriteString("[")
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeKey(b, e, spec)
		}
		b.WriteString("]")
	case map[string]engine.Value:
		b.WriteString("{")
		for i, k := range sortedMapKeys(x) {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(k)
			b.WriteByte('=')
			writeKey(b, x[k], spec)
		}
		b.WriteString("}")
	case engine.Node:
		b.WriteString("N(")
		if spec.IncludeElementIDs {
			b.WriteString(x.ID)
		}
		b.WriteByte(';')
		labels := append([]string(nil), x.Labels...)
		sort.Strings(labels)
		b.WriteString(strings.Join(labels, ","))
		b.WriteByte(';')
		writeKey(b, mapValue(x.Props), spec)
		b.WriteString(")")
	case engine.Rel:
		b.WriteString("R(")
		b.WriteString(x.Type)
		if spec.IncludeElementIDs {
			fmt.Fprintf(b, ";%s;%s->%s", x.ID, x.Start, x.End)
		}
		b.WriteByte(';')
		writeKey(b, mapValue(x.Props), spec)
		b.WriteString(")")
	case engine.Path:
		b.WriteString("P(")
		for i := range x.Nodes {
			writeKey(b, x.Nodes[i], spec)
			if i < len(x.Rels) {
				writeKey(b, x.Rels[i], spec)
			}
		}
		b.WriteString(")")
	default:
		fmt.Fprintf(b, "?:%v", v)
	}
}

// mapValue boxes a property map as a Value so writeKey's map case can render
// it.
func mapValue(m map[string]engine.Value) engine.Value { return m }

// sortedMapKeys returns a map's keys in sorted order for a stable key
// rendering.
func sortedMapKeys(m map[string]engine.Value) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// asFloat returns the float64 magnitude of a numeric value and whether it is
// numeric, covering the integer and float kinds the value model admits (int64
// and float64) plus the int and float widths an adapter might pass through
// before normalization.
func asFloat(v engine.Value) (float64, bool) {
	switch n := v.(type) {
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case float64:
		return n, true
	case float32:
		return float64(n), true
	default:
		return 0, false
	}
}

// isInteger reports whether a numeric value is an integer kind (as opposed to
// a float kind), which floatEqual uses to decide whether coercion is in play.
func isInteger(v engine.Value) bool {
	switch v.(type) {
	case int64, int, int32:
		return true
	default:
		return false
	}
}

// withinTol reports whether two floats are equal within a relative tolerance,
// falling back to an absolute comparison near zero so a reference of exactly
// zero is not unreachable.
func withinTol(a, b, tol float64) bool {
	if a == b {
		return true
	}
	diff := math.Abs(a - b)
	scale := math.Max(math.Abs(a), math.Abs(b))
	if scale == 0 {
		return diff <= tol
	}
	return diff/scale <= tol
}
