// Package dataset owns the canonical CSV layout: it materializes a dataset to
// disk through a Writer, reads one back through Open, verifies it against its
// manifest checksum, and presents it to an adapter as an engine.Dataset. Both
// the synthetic generators (dataset/gen) and the pinned LDBC artifacts
// (dataset/ldbc) produce this one on-disk form, so every adapter has exactly
// one load path and the bytes are byte-identical for every engine, which is
// what makes the same-data fairness rule enforceable.
//
// See notes/Spec/2064g/bench/05-datasets.md for the layout (§1), the checksum
// (§1), and resolution (§5); the Dataset view handed to adapters is fixed by
// 03-engine-spi.md §4.
package dataset

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tamnd/graph-bench/engine"
)

// Set is a materialized, checksum-verified dataset on disk. It implements
// engine.Dataset: an adapter receives it from Open (or from the generate flow)
// and loads from it without knowing or caring whether it was generated or
// fetched.
type Set struct {
	dir string
	m   *engine.Manifest
}

var _ engine.Dataset = (*Set)(nil)

// Open reads the dataset directory at dir, parses its manifest, verifies the
// content checksum against it, and returns it as an engine.Dataset. A checksum
// mismatch is an error, never a warning (spec 05 §5): the bytes on disk are
// not what the manifest claims, so no engine should load them. dir is made
// absolute so an adapter that points a bulk loader at Dir() gets a path that
// survives a working-directory change.
func Open(dir string) (*Set, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	m, err := ReadManifest(abs)
	if err != nil {
		return nil, err
	}
	if err := Verify(abs, m); err != nil {
		return nil, err
	}
	return &Set{dir: abs, m: m}, nil
}

// Name is the dataset's stable name from the manifest.
func (s *Set) Name() string { return s.m.Name }

// Checksum is the content checksum ("sha256:...") from the manifest.
func (s *Set) Checksum() string { return s.m.Checksum }

// Dir is the absolute canonical-layout directory.
func (s *Set) Dir() string { return s.dir }

// Manifest returns the parsed manifest.
func (s *Set) Manifest() *engine.Manifest { return s.m }

// Schema describes every node and rel table with typed columns.
func (s *Set) Schema() engine.Schema { return s.m.SchemaDef }

// NodeFiles returns the absolute file paths for a node label (all shards, in
// the order the manifest records). An unknown label is an error.
func (s *Set) NodeFiles(label string) ([]string, error) {
	ns, ok := s.m.SchemaDef.Nodes[label]
	if !ok {
		return nil, fmt.Errorf("dataset %q: no node label %q", s.m.Name, label)
	}
	return s.absFiles(ns.Files)
}

// RelFiles is the relationship analog of NodeFiles.
func (s *Set) RelFiles(typ string) ([]string, error) {
	rs, ok := s.m.SchemaDef.Rels[typ]
	if !ok {
		return nil, fmt.Errorf("dataset %q: no relationship type %q", s.m.Name, typ)
	}
	return s.absFiles(rs.Files)
}

// Params returns the curated parameter pool for a named key, read from
// params.json beside the dataset (spec 05 §4). An absent file or an unknown
// key returns nil.
func (s *Set) Params(key string) ([]map[string]engine.Value, error) {
	return readParamsPool(filepath.Join(s.dir, "params.json"), key)
}

// Statements returns no statements: a materialized dataset is loaded from its
// CSV files, not by issuing queries.
func (s *Set) Statements() []string { return nil }

// absFiles resolves directory-relative file paths to absolute paths.
func (s *Set) absFiles(rel []string) ([]string, error) {
	if len(rel) == 0 {
		return nil, fmt.Errorf("dataset %q: no files", s.m.Name)
	}
	abs := make([]string, len(rel))
	for i, r := range rel {
		abs[i] = filepath.Join(s.dir, r)
	}
	return abs, nil
}

// ReadHeader reads and parses the first line of a canonical CSV file into
// typed columns, for an adapter that wants the header exactly as written
// rather than the manifest's schema view.
func ReadHeader(path string) ([]engine.Column, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rec, err := csv.NewReader(f).Read()
	if err != nil {
		return nil, fmt.Errorf("dataset: read header of %s: %w", path, err)
	}
	return ParseHeader(rec)
}
