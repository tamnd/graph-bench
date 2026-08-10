package engine

import "fmt"

// StatementsDataset is a Dataset built from setup statements instead of
// files, for workloads that seed their own data (micro-write, linkbench
// at smoke scale). File accessors fail loudly rather than returning
// empty success.
type StatementsDataset struct {
	name   string
	stmts  []string
	schema Schema
}

// NewStatements builds a statements-only dataset.
func NewStatements(name string, schema Schema, stmts []string) *StatementsDataset {
	return &StatementsDataset{name: name, stmts: stmts, schema: schema}
}

func (s *StatementsDataset) Name() string     { return s.name }
func (s *StatementsDataset) Checksum() string { return "" }
func (s *StatementsDataset) Dir() string      { return "" }
func (s *StatementsDataset) Manifest() *Manifest {
	return &Manifest{Name: s.name, Kind: "statements", ListDelimiter: ";", Null: ""}
}
func (s *StatementsDataset) Schema() Schema { return s.schema }
func (s *StatementsDataset) NodeFiles(label string) ([]string, error) {
	return nil, fmt.Errorf("statements dataset %q has no node files (label %q)", s.name, label)
}
func (s *StatementsDataset) RelFiles(typ string) ([]string, error) {
	return nil, fmt.Errorf("statements dataset %q has no rel files (type %q)", s.name, typ)
}
func (s *StatementsDataset) Params(string) ([]map[string]Value, error) { return nil, nil }
func (s *StatementsDataset) Statements() []string                      { return s.stmts }
