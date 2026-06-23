package target

import "fmt"

// StatementsDataset is a Dataset built from openCypher statements rather than
// from canonical CSV files. It is the load path for an engine without a bulk
// CSV loader and the convenient shape for the small datasets used in tests. It
// has no directory, no checksum, and an empty schema; the file-backed accessors
// report that there are no files. The real, materialized datasets come from the
// dataset package and carry a manifest and a checksum.
type StatementsDataset struct {
	name  string
	stmts []string
}

// NewStatements returns a Dataset that builds its graph by issuing the given
// statements in order under the name.
func NewStatements(name string, stmts []string) *StatementsDataset {
	return &StatementsDataset{name: name, stmts: stmts}
}

var _ Dataset = (*StatementsDataset)(nil)

func (d *StatementsDataset) Name() string     { return d.name }
func (d *StatementsDataset) Checksum() string { return "" }
func (d *StatementsDataset) Dir() string      { return "" }

// Manifest reports a minimal manifest: a statements dataset has no recipe and
// no checksum, only its name and the two encoding conventions every dataset
// carries.
func (d *StatementsDataset) Manifest() *Manifest {
	return &Manifest{Name: d.name, Kind: "statements", ListDelimiter: ";", Null: "empty"}
}

// Schema is empty: a statements dataset declares no labels or types up front.
func (d *StatementsDataset) Schema() Schema { return Schema{} }

// NodeFiles and RelFiles have nothing to return; a statements dataset is not
// file-backed. They report the absence rather than an empty success so an
// adapter that took the file path by mistake fails loudly.
func (d *StatementsDataset) NodeFiles(label string) ([]string, []Column, error) {
	return nil, nil, fmt.Errorf("statements dataset %q has no node files for %q", d.name, label)
}

func (d *StatementsDataset) RelFiles(typ string) ([]string, []Column, error) {
	return nil, nil, fmt.Errorf("statements dataset %q has no relationship files for %q", d.name, typ)
}

// Params returns nil: a statements dataset carries no curated parameters.
func (d *StatementsDataset) Params(workload string) (Params, error) { return nil, nil }

// Statements returns the build statements in order.
func (d *StatementsDataset) Statements() []string { return d.stmts }
