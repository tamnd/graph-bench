package dataset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/tamnd/graph-bench/engine"
)

// Layout constants for a dataset directory: the manifest file and the two
// subdirectories that hold the node and relationship CSV files (spec 05 §1).
const (
	manifestName = "manifest.json"
	nodesDir     = "nodes"
	relsDir      = "rels"
)

// WriteManifest writes a manifest to manifest.json in a dataset directory as
// indented JSON, the one file a human reads to know what a dataset is. It
// always writes the v0.3 form of engine.Manifest; the v1 form is accepted on
// read only.
func WriteManifest(dir string, m *engine.Manifest) error {
	blob, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	blob = append(blob, '\n')
	return os.WriteFile(filepath.Join(dir, manifestName), blob, 0o644)
}

// ReadManifest reads and parses the manifest.json in a dataset directory. It
// accepts both the v0.3 form (engine.Manifest verbatim) and the v1 form
// (integer generatorVersion, typed params, top-level nodeCount/edgeCount, and
// a schema block keyed "relationships" with "file" lists and string ids), so
// datasets materialized by v0.2 still open and verify (spec 05 §1: v0.2
// checksums remain valid).
func ReadManifest(dir string) (*engine.Manifest, error) {
	blob, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, err
	}
	var raw manifestJSON
	if err := json.Unmarshal(blob, &raw); err != nil {
		return nil, fmt.Errorf("dataset: parse %s: %w", manifestName, err)
	}
	m, err := raw.manifest()
	if err != nil {
		return nil, fmt.Errorf("dataset: parse %s: %w", manifestName, err)
	}
	return m, nil
}

// manifestJSON is the union of the v1 and v0.3 manifest forms. Polymorphic
// fields are held raw and normalized by manifest().
type manifestJSON struct {
	Name             string                     `json:"name"`
	Kind             string                     `json:"kind"`
	Generator        string                     `json:"generator"`
	GeneratorVersion json.RawMessage            `json:"generatorVersion"`
	Seed             int64                      `json:"seed"`
	Params           map[string]json.RawMessage `json:"params"`
	Scale            string                     `json:"scale"`
	ListDelimiter    string                     `json:"listDelimiter"`
	Null             string                     `json:"null"`
	Checksum         string                     `json:"checksum"`
	NodeCount        int64                      `json:"nodeCount"` // v1: top level
	EdgeCount        int64                      `json:"edgeCount"` // v1: top level
	Invariants       invariantsJSON             `json:"invariants"`
	Schema           schemaJSON                 `json:"schema"`
}

// invariantsJSON reads both forms: v1 used pointers for every field, v0.3
// makes the counts plain int64.
type invariantsJSON struct {
	NodeCount     *int64 `json:"nodeCount"`
	EdgeCount     *int64 `json:"edgeCount"`
	TriangleCount *int64 `json:"triangleCount"`
	Diameter      *int64 `json:"diameter"`
}

// schemaJSON reads both schema forms: v0.3 keys the relationship tables
// "rels", v1 keyed them "relationships".
type schemaJSON struct {
	Nodes         map[string]nodeSchemaJSON `json:"nodes"`
	Rels          map[string]relSchemaJSON  `json:"rels"`
	Relationships map[string]relSchemaJSON  `json:"relationships"`
}

// nodeSchemaJSON reads both node-table forms: v1 tagged the file list "file"
// and recorded the id as the bare column name; v0.3 uses "files" and a typed
// Column.
type nodeSchemaJSON struct {
	Files      []string        `json:"files"`
	File       []string        `json:"file"` // v1 tag
	ID         json.RawMessage `json:"id"`   // string (v1) or Column (v0.3)
	Properties []engine.Column `json:"properties"`
	Labels     []string        `json:"labels"`
}

// relSchemaJSON reads both rel-table forms ("files" vs the v1 "file" tag).
type relSchemaJSON struct {
	Files      []string        `json:"files"`
	File       []string        `json:"file"` // v1 tag
	Start      string          `json:"start"`
	End        string          `json:"end"`
	Properties []engine.Column `json:"properties"`
}

// manifest normalizes the raw union into an engine.Manifest.
func (raw *manifestJSON) manifest() (*engine.Manifest, error) {
	m := &engine.Manifest{
		Name:          raw.Name,
		Kind:          raw.Kind,
		Generator:     raw.Generator,
		Seed:          raw.Seed,
		Scale:         raw.Scale,
		ListDelimiter: raw.ListDelimiter,
		Null:          raw.Null,
		Checksum:      raw.Checksum,
	}

	gv, err := versionString(raw.GeneratorVersion)
	if err != nil {
		return nil, err
	}
	m.GeneratorVersion = gv

	if raw.Params != nil {
		m.Params = make(map[string]string, len(raw.Params))
		for k, v := range raw.Params {
			s, err := paramString(v)
			if err != nil {
				return nil, fmt.Errorf("param %q: %w", k, err)
			}
			m.Params[k] = s
		}
	}

	// Counts: prefer the invariants block, fall back to the v1 top-level
	// fields when the block omits them.
	m.Invariants.NodeCount = coalesce(raw.Invariants.NodeCount, raw.NodeCount)
	m.Invariants.EdgeCount = coalesce(raw.Invariants.EdgeCount, raw.EdgeCount)
	m.Invariants.TriangleCount = raw.Invariants.TriangleCount
	m.Invariants.Diameter = raw.Invariants.Diameter

	m.SchemaDef = engine.Schema{
		Nodes: map[string]engine.NodeSchema{},
		Rels:  map[string]engine.RelSchema{},
	}
	for label, ns := range raw.Schema.Nodes {
		id, err := idColumnJSON(ns.ID)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", label, err)
		}
		m.SchemaDef.Nodes[label] = engine.NodeSchema{
			Files:      firstNonEmpty(ns.Files, ns.File),
			ID:         id,
			Properties: ns.Properties,
			Labels:     ns.Labels,
		}
	}
	rels := raw.Schema.Rels
	if rels == nil {
		rels = raw.Schema.Relationships
	}
	for typ, rs := range rels {
		m.SchemaDef.Rels[typ] = engine.RelSchema{
			Files:      firstNonEmpty(rs.Files, rs.File),
			Start:      rs.Start,
			End:        rs.End,
			Properties: rs.Properties,
		}
	}
	return m, nil
}

// versionString normalizes the generatorVersion field: absent stays empty, a
// JSON number (the v1 form) becomes its decimal string, a JSON string (the
// v0.3 form) is taken as-is.
func versionString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", fmt.Errorf("generatorVersion: %w", err)
		}
		return s, nil
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return "", fmt.Errorf("generatorVersion: %w", err)
	}
	return strconv.FormatInt(n, 10), nil
}

// paramString normalizes a param value to the canonical string form: a JSON
// string is unquoted, everything else (number, bool, array) is its compact
// JSON text. The compact text round-trips through typedParam back to the exact
// v1 typed value, which is what keeps v1 checksums verifiable (see
// checksum.go).
func paramString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		return s, nil
	}
	var buf bytes.Buffer
	// Compact strips insignificant whitespace only, so the number tokens an
	// indented manifest carries survive byte-for-byte into the canonical
	// string form.
	if err := json.Compact(&buf, raw); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// idColumnJSON reads the node id in either form: the v1 bare column-name
// string becomes a typed ID column, the v0.3 object is taken as-is. An absent
// id stays zero.
func idColumnJSON(raw json.RawMessage) (engine.Column, error) {
	if len(raw) == 0 {
		return engine.Column{}, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return engine.Column{}, err
		}
		return engine.Column{Name: s, Type: "ID"}, nil
	}
	var c engine.Column
	if err := json.Unmarshal(raw, &c); err != nil {
		return engine.Column{}, err
	}
	return c, nil
}

// coalesce prefers a present, non-zero pointer value over the fallback.
func coalesce(p *int64, fallback int64) int64 {
	if p != nil && *p != 0 {
		return *p
	}
	return fallback
}

// firstNonEmpty returns a when it has entries, else b.
func firstNonEmpty(a, b []string) []string {
	if len(a) > 0 {
		return a
	}
	return b
}
