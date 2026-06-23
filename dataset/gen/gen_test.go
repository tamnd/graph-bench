package gen

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/tamnd/graph-bench/target"
)

// memWriter is an in-memory gen.Writer for the tests: it captures each file's
// rows so a test can assert on the emitted bytes and counts without touching the
// disk. It is intentionally separate from the dataset package's file Writer so
// the generators are tested in isolation from the on-disk format.
type memWriter struct {
	files map[string]*memRows // keyed by "nodes/<label>" or "rels/<type>"
	nodes int64
	edges int64
}

type memRows struct {
	header []target.Column
	rows   [][]string
	count  *int64
}

func newMemWriter() *memWriter { return &memWriter{files: map[string]*memRows{}} }

func (w *memWriter) NodeFile(label string, header []target.Column) (RowWriter, error) {
	mr := &memRows{header: header, count: &w.nodes}
	w.files["nodes/"+label] = mr
	return mr, nil
}

func (w *memWriter) RelFile(typ string, header []target.Column) (RowWriter, error) {
	mr := &memRows{header: header, count: &w.edges}
	w.files["rels/"+typ] = mr
	return mr, nil
}

func (w *memWriter) Finalize(partial *target.Manifest) (*target.Manifest, error) {
	m := *partial
	m.NodeCount = w.nodes
	m.EdgeCount = w.edges
	return &m, nil
}

func (mr *memRows) Write(cells []string) error {
	row := make([]string, len(cells))
	copy(row, cells)
	mr.rows = append(mr.rows, row)
	*mr.count++
	return nil
}

func (mr *memRows) Close() error { return nil }

// bytesOf renders a captured writer to a stable string for byte-identity
// comparison: files in sorted order, header then rows, each row comma-joined.
func (w *memWriter) bytesOf() string {
	var keys []string
	for k := range w.files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	for _, k := range keys {
		mr := w.files[k]
		fmt.Fprintf(&b, "== %s ==\n", k)
		hdr := make([]string, len(mr.header))
		for i, c := range mr.header {
			hdr[i] = c.Name + ":" + c.Type
		}
		b.WriteString(strings.Join(hdr, ",") + "\n")
		for _, row := range mr.rows {
			b.WriteString(strings.Join(row, ",") + "\n")
		}
	}
	return b.String()
}

// run generates a config into a fresh memWriter and returns it with the manifest.
func run(t *testing.T, cfg Config) (*memWriter, *target.Manifest) {
	t.Helper()
	w := newMemWriter()
	m, err := Generate(context.Background(), cfg, w)
	if err != nil {
		t.Fatalf("Generate(%s): %v", cfg.Kind, err)
	}
	return w, m
}

// TestDeterminism is the bit-reproducibility contract: the same config produces
// byte-identical output on two independent runs. This is what the regression
// gate depends on (spec doc 04 section 3).
func TestDeterminism(t *testing.T) {
	cfgs := []Config{
		{Kind: "uniform", Seed: 42, N: 500, Degree: 6},
		{Kind: "powerlaw", Seed: 7, N: 500, Gamma: 2.5, MinDeg: 1, MaxDeg: 50},
		{Kind: "er", Seed: 99, N: 300, P: 0.02},
		{Kind: "grid", Seed: 1, Rows: 20, Cols: 30},
		{Kind: "grid", Seed: 1, Rows: 20, Cols: 30, Diagonal: true},
		{Kind: "rmat", Seed: 123, Scale: 10, EdgeFactor: 8},
	}
	for _, cfg := range cfgs {
		t.Run(cfg.Kind, func(t *testing.T) {
			w1, _ := run(t, cfg)
			w2, _ := run(t, cfg)
			if w1.bytesOf() != w2.bytesOf() {
				t.Errorf("%s: two runs of the same config produced different bytes", cfg.Kind)
			}
		})
	}
}

// TestSeedChangesOutput confirms a different seed changes the edges for the
// random generators, so the seed is actually threaded through.
func TestSeedChangesOutput(t *testing.T) {
	a, _ := run(t, Config{Kind: "uniform", Seed: 1, N: 500, Degree: 6})
	b, _ := run(t, Config{Kind: "uniform", Seed: 2, N: 500, Degree: 6})
	if a.bytesOf() == b.bytesOf() {
		t.Error("uniform: two different seeds produced identical bytes")
	}
}

// TestUniformInvariants checks the closed-form node and edge counts: N nodes,
// exactly N*Degree edges, every node with the right out-degree.
func TestUniformInvariants(t *testing.T) {
	cfg := Config{Kind: "uniform", Seed: 42, N: 1000, Degree: 7}
	w, m := run(t, cfg)
	if m.NodeCount != cfg.N {
		t.Errorf("NodeCount = %d, want %d", m.NodeCount, cfg.N)
	}
	wantEdges := cfg.N * int64(cfg.Degree)
	if m.EdgeCount != wantEdges {
		t.Errorf("EdgeCount = %d, want %d", m.EdgeCount, wantEdges)
	}
	if got := *m.Invariants.EdgeCount; got != wantEdges {
		t.Errorf("Invariants.EdgeCount = %d, want %d", got, wantEdges)
	}
	// No self-loops, no duplicate edges, and each source has exactly Degree.
	rels := w.files["rels/"+relType]
	perSource := map[string]map[string]bool{}
	for _, row := range rels.rows {
		u, v := row[0], row[1]
		if u == v {
			t.Fatalf("self-loop at %s", u)
		}
		if perSource[u] == nil {
			perSource[u] = map[string]bool{}
		}
		if perSource[u][v] {
			t.Fatalf("duplicate edge %s->%s", u, v)
		}
		perSource[u][v] = true
	}
	for u, targets := range perSource {
		if len(targets) != cfg.Degree {
			t.Fatalf("node %s has out-degree %d, want %d", u, len(targets), cfg.Degree)
		}
	}
}

// TestGridInvariants checks the grid's closed-form edge count, diameter, and
// triangle count, the arithmetic that makes the grid a validation fixture.
func TestGridInvariants(t *testing.T) {
	rows, cols := int64(40), int64(25)
	_, m := run(t, Config{Kind: "grid", Rows: int(rows), Cols: int(cols)})
	wantNodes := rows * cols
	wantEdges := rows*(cols-1) + cols*(rows-1)
	if m.NodeCount != wantNodes {
		t.Errorf("NodeCount = %d, want %d", m.NodeCount, wantNodes)
	}
	if m.EdgeCount != wantEdges {
		t.Errorf("EdgeCount = %d, want %d", m.EdgeCount, wantEdges)
	}
	if got := *m.Invariants.Diameter; got != (rows-1)+(cols-1) {
		t.Errorf("Diameter = %d, want %d", got, (rows-1)+(cols-1))
	}
	if got := *m.Invariants.TriangleCount; got != 0 {
		t.Errorf("TriangleCount = %d, want 0 (4-neighbor grid is bipartite)", got)
	}
}

// TestRMATInvariants checks RMAT's node and edge counts and that every endpoint
// is within the 2^Scale id space.
func TestRMATInvariants(t *testing.T) {
	cfg := Config{Kind: "rmat", Seed: 123, Scale: 12, EdgeFactor: 8}
	w, m := run(t, cfg)
	wantNodes := int64(1) << uint(cfg.Scale)
	wantEdges := int64(cfg.EdgeFactor) * wantNodes
	if m.NodeCount != wantNodes {
		t.Errorf("NodeCount = %d, want %d", m.NodeCount, wantNodes)
	}
	if m.EdgeCount != wantEdges {
		t.Errorf("EdgeCount = %d, want %d", m.EdgeCount, wantEdges)
	}
	// Edges are sorted by (source, target); confirm the order and the id range.
	rels := w.files["rels/"+relType]
	var prevU, prevV int64 = -1, -1
	for _, row := range rels.rows {
		u := mustAtoi(t, row[0])
		v := mustAtoi(t, row[1])
		if u < 0 || u >= wantNodes || v < 0 || v >= wantNodes {
			t.Fatalf("edge %d->%d outside id space [0,%d)", u, v, wantNodes)
		}
		if u < prevU || (u == prevU && v < prevV) {
			t.Fatalf("edges not sorted: %d->%d after %d->%d", u, v, prevU, prevV)
		}
		prevU, prevV = u, v
	}
}

// TestERExpectedEdgeCount checks the realized edge count is within a few standard
// deviations of the closed-form expectation p*N*(N-1), so the geometric-skip
// sampler matches G(n,p).
func TestERExpectedEdgeCount(t *testing.T) {
	cfg := Config{Kind: "er", Seed: 5, N: 2000, P: 0.01}
	_, m := run(t, cfg)
	nf := float64(cfg.N)
	expected := cfg.P * nf * (nf - 1)
	stddev := expected // variance ~ expected for small p; use a loose 5-sigma-ish band
	low := expected - 0.2*stddev
	high := expected + 0.2*stddev
	got := float64(m.EdgeCount)
	if got < low || got > high {
		t.Errorf("EdgeCount = %d, want within [%.0f, %.0f] of expected %.0f", m.EdgeCount, low, high, expected)
	}
}

// TestUnknownGenerator confirms an unknown kind is a clear error, not a panic.
func TestUnknownGenerator(t *testing.T) {
	if _, err := New("nope"); err == nil {
		t.Error("New(\"nope\") returned nil error, want unknown-generator error")
	}
}

func mustAtoi(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	if _, err := fmt.Sscan(s, &n); err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return n
}
