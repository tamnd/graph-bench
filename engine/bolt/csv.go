//go:build bolt

package bolt

import (
	"fmt"
	"os"
	"strings"

	"github.com/tamnd/graph-bench/engine"
)

// Column describes one column of a canonical-layout dataset CSV. Type is
// either a structural marker (ID, START_ID, END_ID, LABEL, TYPE) or a value
// type from the dataset schema (STRING, INT64, FLOAT64, BOOL, DATE,
// DATETIME, STRING[]). Structural marker columns other than ID have an
// empty Name.
type Column struct {
	Name string
	Type string
}

// Structural reports whether the column is a structural marker rather than
// a property column.
func (c Column) Structural() bool {
	switch c.Type {
	case "ID", "START_ID", "END_ID", "LABEL", "TYPE":
		return true
	}
	return false
}

// ParseHeader parses a canonical CSV header line ("id:ID,:LABEL" or
// ":START_ID,:END_ID,weight:FLOAT64") into ordered columns. Unannotated
// column names take their type from propTypes, defaulting to STRING.
func ParseHeader(header string, propTypes map[string]string) []Column {
	fields := strings.Split(header, ",")
	cols := make([]Column, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if i := strings.IndexByte(f, ':'); i >= 0 {
			cols = append(cols, Column{Name: f[:i], Type: f[i+1:]})
			continue
		}
		typ := propTypes[f]
		if typ == "" {
			typ = "STRING"
		}
		cols = append(cols, Column{Name: f, Type: typ})
	}
	return cols
}

// PropTypes builds the name-to-type map for ParseHeader from schema columns.
func PropTypes(cols []engine.Column) map[string]string {
	m := make(map[string]string, len(cols))
	for _, c := range cols {
		m[c.Name] = c.Type
	}
	return m
}

// ReadCSV reads a canonical CSV file and returns its parsed header columns
// and raw data rows (header stripped). An empty or header-only file returns
// zero rows.
func ReadCSV(path string, propTypes map[string]string) ([]Column, []string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("bolt: read %s: %w", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return nil, nil, nil
	}
	return ParseHeader(lines[0], propTypes), lines[1:], nil
}
