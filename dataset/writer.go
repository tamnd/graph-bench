package dataset

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/target"
)

// Writer is the concrete canonical-layout sink the generators write through. It
// creates the nodes/ and rels/ subdirectories under a dataset directory, opens
// one CSV file per label and per type with a typed header, counts the rows, and
// in Finalize builds the schema, totals, and content checksum and writes the
// manifest. It implements gen.Writer so any generator emits through it and the
// on-disk form is identical regardless of which generator produced it.
type Writer struct {
	dir string

	nodeSchema map[string]target.NodeSchema
	relSchema  map[string]target.RelSchema
	nodeCount  int64
	edgeCount  int64
}

var _ gen.Writer = (*Writer)(nil)

// NewWriter prepares a dataset directory at dir: it creates dir/nodes and
// dir/rels and returns a Writer rooted there. An existing directory is reused;
// its files are overwritten as labels and types are opened.
func NewWriter(dir string) (*Writer, error) {
	for _, sub := range []string{nodesDir, relsDir} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	return &Writer{
		dir:        dir,
		nodeSchema: map[string]target.NodeSchema{},
		relSchema:  map[string]target.RelSchema{},
	}, nil
}

// NodeFile opens the node file for a label, writes its typed header, and returns
// a RowWriter for the node rows. It records the label's schema (the file, the id
// column, and the property columns) for the manifest.
func (w *Writer) NodeFile(label string, header []target.Column) (gen.RowWriter, error) {
	rel := nodesDir + "/" + label + ".csv"
	rw, err := w.openFile(rel, header, &w.nodeCount)
	if err != nil {
		return nil, err
	}
	id, err := idColumn(header)
	if err != nil {
		return nil, err
	}
	w.nodeSchema[label] = target.NodeSchema{
		Files:      []string{rel},
		ID:         id,
		Properties: propertyColumns(header),
		Labels:     []string{label},
	}
	return rw, nil
}

// RelFile opens the relationship file for a type, writes its typed header, and
// returns a RowWriter for the edge rows. The endpoint labels are filled in by
// Finalize, once every node label is known.
func (w *Writer) RelFile(typ string, header []target.Column) (gen.RowWriter, error) {
	rel := relsDir + "/" + typ + ".csv"
	rw, err := w.openFile(rel, header, &w.edgeCount)
	if err != nil {
		return nil, err
	}
	w.relSchema[typ] = target.RelSchema{
		Files:      []string{rel},
		Properties: propertyColumns(header),
	}
	return rw, nil
}

// openFile creates a CSV file at the directory-relative path, writes the typed
// header, and returns a rowWriter that appends to the running count.
func (w *Writer) openFile(rel string, header []target.Column, count *int64) (*rowWriter, error) {
	f, err := os.Create(filepath.Join(w.dir, rel))
	if err != nil {
		return nil, err
	}
	cw := csv.NewWriter(f)
	if err := cw.Write(FormatHeader(header)); err != nil {
		f.Close()
		return nil, err
	}
	return &rowWriter{f: f, cw: cw, count: count}, nil
}

// Finalize fills in the relationship endpoint labels, sets the totals and the
// encoding conventions, writes the manifest, computes the content checksum,
// rewrites the manifest with it, and returns the complete manifest. The partial
// manifest carries the recipe (generator, version, seed, params, invariants);
// Finalize owns the schema, the counts, and the checksum.
func (w *Writer) Finalize(partial *target.Manifest) (*target.Manifest, error) {
	m := *partial
	if m.ListDelimiter == "" {
		m.ListDelimiter = ";"
	}
	if m.Null == "" {
		m.Null = "empty"
	}
	m.NodeCount = w.nodeCount
	m.EdgeCount = w.edgeCount

	// Every synthetic generator emits a single node label, so the relationship
	// endpoints are that label. When there is exactly one, fill it in; otherwise
	// leave the endpoints blank for an adapter that does not need them.
	var soleLabel string
	if len(w.nodeSchema) == 1 {
		for label := range w.nodeSchema {
			soleLabel = label
		}
	}
	rels := make(map[string]target.RelSchema, len(w.relSchema))
	for typ, rs := range w.relSchema {
		if soleLabel != "" {
			rs.Start, rs.End = soleLabel, soleLabel
		}
		rels[typ] = rs
	}
	m.Schema = target.Schema{Nodes: w.nodeSchema, Relationships: rels}

	// Write the manifest once without the checksum, compute the checksum over the
	// files plus the recipe, then write it again with the checksum recorded.
	if err := WriteManifest(w.dir, &m); err != nil {
		return nil, err
	}
	sum, err := Checksum(w.dir, &m)
	if err != nil {
		return nil, err
	}
	m.Checksum = sum
	if err := WriteManifest(w.dir, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// rowWriter writes the rows of one CSV file and counts them. Close flushes the
// buffered CSV writer and closes the file.
type rowWriter struct {
	f     *os.File
	cw    *csv.Writer
	count *int64
}

var _ gen.RowWriter = (*rowWriter)(nil)

func (r *rowWriter) Write(cells []string) error {
	if err := r.cw.Write(cells); err != nil {
		return err
	}
	*r.count++
	return nil
}

func (r *rowWriter) Close() error {
	r.cw.Flush()
	if err := r.cw.Error(); err != nil {
		r.f.Close()
		return err
	}
	return r.f.Close()
}

// DirName returns the canonical directory name for a dataset: the manifest name
// followed by the first eight hex characters of its checksum, the
// <name>-<checksum8> form. It is the cache-key-derived directory name a
// materialized dataset lives under.
func DirName(m *target.Manifest) string {
	sum := m.Checksum
	const prefix = "sha256:"
	if len(sum) > len(prefix) {
		sum = sum[len(prefix):]
	}
	if len(sum) > 8 {
		sum = sum[:8]
	}
	if sum == "" {
		return m.Name
	}
	return fmt.Sprintf("%s-%s", m.Name, sum)
}
