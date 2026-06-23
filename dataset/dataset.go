// Package dataset owns the canonical CSV layout: it materializes a dataset to
// disk through a Writer, reads one back through Open, verifies it against its
// manifest checksum, and presents it to an adapter as a target.Dataset. Both the
// synthetic generators (dataset/gen) and, later, the pinned LDBC artifacts
// (dataset/ldbc) produce this one on-disk form, so every adapter has exactly one
// load path to write and the bytes are byte-identical for every engine, which is
// what makes the same-data fairness rule enforceable.
//
// See notes/Spec/2060/bench/04-datasets-and-generation.md for the layout
// (section 1), the manifest (section 1.5), the checksum (section 3.4), and the
// Dataset view this package hands to adapters (section 6).
package dataset

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tamnd/graph-bench/target"
)

// Set is a materialized, checksum-verified dataset on disk. It implements
// target.Dataset: an adapter receives it from Open (or from the generate flow)
// and loads from it without knowing or caring whether it was generated or
// fetched.
type Set struct {
	dir string
	m   *target.Manifest
}

var _ target.Dataset = (*Set)(nil)

// Open reads the dataset directory at dir, parses its manifest, verifies the
// content checksum against it, and returns it as a target.Dataset. A checksum
// mismatch is an error: the bytes on disk are not what the manifest claims, so
// no engine should load them. dir is made absolute so an adapter that points a
// bulk loader at Dir() gets a path that survives a working-directory change.
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

func (s *Set) Name() string               { return s.m.Name }
func (s *Set) Checksum() string           { return s.m.Checksum }
func (s *Set) Dir() string                { return s.dir }
func (s *Set) Manifest() *target.Manifest { return s.m }
func (s *Set) Schema() target.Schema      { return s.m.Schema }

// NodeFiles returns the absolute file paths for a node label (all shards, in the
// order the manifest records) and the parsed typed header read from the first
// shard. An unknown label is an error.
func (s *Set) NodeFiles(label string) ([]string, []target.Column, error) {
	ns, ok := s.m.Schema.Nodes[label]
	if !ok {
		return nil, nil, fmt.Errorf("dataset %q: no node label %q", s.m.Name, label)
	}
	return s.filesAndHeader(ns.Files)
}

// RelFiles is the relationship analog of NodeFiles.
func (s *Set) RelFiles(typ string) ([]string, []target.Column, error) {
	rs, ok := s.m.Schema.Relationships[typ]
	if !ok {
		return nil, nil, fmt.Errorf("dataset %q: no relationship type %q", s.m.Name, typ)
	}
	return s.filesAndHeader(rs.Files)
}

// Params returns the curated parameter pool for a named key, read from
// params.json beside the dataset. An absent file or an unknown key returns nil.
func (s *Set) Params(key string) ([]target.Params, error) {
	return readParamsPool(filepath.Join(s.dir, "params.json"), key)
}

// Statements returns no statements: a materialized dataset is loaded from its
// CSV files, not by issuing queries.
func (s *Set) Statements() []string { return nil }

// filesAndHeader resolves directory-relative file paths to absolute paths and
// reads the typed header from the first one, so an adapter gets both the files
// to load and the columns to interpret them.
func (s *Set) filesAndHeader(rel []string) ([]string, []target.Column, error) {
	if len(rel) == 0 {
		return nil, nil, fmt.Errorf("dataset %q: no files", s.m.Name)
	}
	abs := make([]string, len(rel))
	for i, r := range rel {
		abs[i] = filepath.Join(s.dir, r)
	}
	cols, err := readHeader(abs[0])
	if err != nil {
		return nil, nil, err
	}
	return abs, cols, nil
}

// readHeader reads and parses the first line of a canonical CSV file into typed
// columns.
func readHeader(path string) ([]target.Column, error) {
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
