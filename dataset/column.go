package dataset

import (
	"fmt"
	"strings"

	"github.com/tamnd/graph-bench/target"
)

// structuralTypes are the header type tokens that are not properties: they carry
// load-time structure (the id, the labels, the endpoints, the type) rather than
// a value a query reads. A column with one of these types has an empty Name for
// the bare forms (:LABEL, :TYPE, :START_ID, :END_ID) or a named form for :ID
// (id:ID), and is excluded from a file's property list.
var structuralTypes = map[string]bool{
	"ID":       true,
	"LABEL":    true,
	"TYPE":     true,
	"START_ID": true,
	"END_ID":   true,
}

// FormatHeader renders typed columns into the CSV header cells. A named column
// is "name:TYPE"; a bare structural column is ":TYPE". This is the inverse of
// ParseHeader and the form gr import and the other bulk loaders parse.
func FormatHeader(cols []target.Column) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		if c.Name == "" {
			out[i] = ":" + c.Type
		} else {
			out[i] = c.Name + ":" + c.Type
		}
	}
	return out
}

// ParseHeader parses CSV header cells into typed columns. A cell with a colon
// splits into a name and a type (the name may be empty for the bare structural
// forms); a cell with no colon is an untyped property, read as STRING. The type
// token is upper-cased so the type set is matched case-insensitively.
func ParseHeader(cells []string) ([]target.Column, error) {
	cols := make([]target.Column, len(cells))
	for i, cell := range cells {
		cell = strings.TrimSpace(cell)
		if cell == "" {
			return nil, fmt.Errorf("dataset: empty header cell at index %d", i)
		}
		name, typ, hasColon := strings.Cut(cell, ":")
		if !hasColon {
			cols[i] = target.Column{Name: cell, Type: "STRING"}
			continue
		}
		cols[i] = target.Column{Name: name, Type: strings.ToUpper(typ)}
	}
	return cols, nil
}

// IsStructural reports whether a column carries load-time structure rather than
// a property value, so a schema builder can separate the id and label columns
// from the property columns.
func IsStructural(c target.Column) bool { return structuralTypes[c.Type] }

// idColumn returns the name of the :ID column in a node header, or an error if
// there is none. The canonical node header always has exactly one.
func idColumn(cols []target.Column) (string, error) {
	for _, c := range cols {
		if c.Type == "ID" {
			return c.Name, nil
		}
	}
	return "", fmt.Errorf("dataset: node header has no :ID column")
}

// propertyColumns returns the non-structural columns of a header, in order, the
// columns a loader reads as values.
func propertyColumns(cols []target.Column) []target.Column {
	var props []target.Column
	for _, c := range cols {
		if !IsStructural(c) {
			props = append(props, c)
		}
	}
	return props
}
