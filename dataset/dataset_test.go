package dataset

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/target"
)

// generate materializes a config into a fresh temp directory and returns the
// dataset directory and the manifest. It is the test's stand-in for the generate
// verb's staging-and-name flow.
func generate(t *testing.T, cfg gen.Config) (string, *target.Manifest) {
	t.Helper()
	dir := t.TempDir()
	w, err := NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	m, err := gen.Generate(context.Background(), cfg, w)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return dir, m
}

// TestWriteAndOpen is the round trip: generate a dataset, open it back, and check
// the manifest, schema, and file accessors all reflect what was written.
func TestWriteAndOpen(t *testing.T) {
	dir, m := generate(t, gen.Config{Kind: "grid", Rows: 10, Cols: 12})

	ds, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if ds.Name() != m.Name {
		t.Errorf("Name = %q, want %q", ds.Name(), m.Name)
	}
	if ds.Checksum() != m.Checksum || m.Checksum == "" {
		t.Errorf("Checksum = %q, manifest %q", ds.Checksum(), m.Checksum)
	}
	if ds.Manifest().NodeCount != 120 {
		t.Errorf("NodeCount = %d, want 120", ds.Manifest().NodeCount)
	}

	files, cols, err := ds.NodeFiles("Node")
	if err != nil {
		t.Fatalf("NodeFiles: %v", err)
	}
	if len(files) != 1 || filepath.Base(files[0]) != "Node.csv" {
		t.Errorf("NodeFiles = %v, want one Node.csv", files)
	}
	if !filepath.IsAbs(files[0]) {
		t.Errorf("NodeFiles path %q is not absolute", files[0])
	}
	// The node header is id:ID,:LABEL, so two columns and an ID column present.
	if len(cols) != 2 || cols[0].Type != "ID" || cols[1].Type != "LABEL" {
		t.Errorf("node header = %v, want [id:ID :LABEL]", cols)
	}

	if _, _, err := ds.RelFiles("EDGE"); err != nil {
		t.Fatalf("RelFiles: %v", err)
	}
	if _, _, err := ds.NodeFiles("Nope"); err == nil {
		t.Error("NodeFiles(unknown) returned nil error")
	}
}

// TestChecksumStable confirms the checksum is reproducible: regenerating the same
// recipe into a different directory yields the same checksum, and a different
// recipe yields a different one.
func TestChecksumStable(t *testing.T) {
	_, a := generate(t, gen.Config{Kind: "uniform", Seed: 3, N: 200, Degree: 4})
	_, b := generate(t, gen.Config{Kind: "uniform", Seed: 3, N: 200, Degree: 4})
	if a.Checksum != b.Checksum {
		t.Errorf("same recipe gave different checksums: %s vs %s", a.Checksum, b.Checksum)
	}
	_, c := generate(t, gen.Config{Kind: "uniform", Seed: 4, N: 200, Degree: 4})
	if a.Checksum == c.Checksum {
		t.Error("different seed gave the same checksum")
	}
}

// TestVerifyDetectsTampering confirms Open fails when a data file is altered
// after the manifest's checksum was written, which is the corruption guard F2
// relies on.
func TestVerifyDetectsTampering(t *testing.T) {
	dir, _ := generate(t, gen.Config{Kind: "grid", Rows: 5, Cols: 5})
	// Append a byte to a data file so the content no longer matches the manifest.
	path := filepath.Join(dir, "rels", "EDGE.csv")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for tamper: %v", err)
	}
	if _, err := f.WriteString("999,999,EDGE\n"); err != nil {
		t.Fatalf("tamper write: %v", err)
	}
	f.Close()

	if _, err := Open(dir); err == nil {
		t.Error("Open succeeded on a tampered dataset, want a checksum mismatch")
	}
}

// TestDirName checks the <name>-<checksum8> directory name derivation.
func TestDirName(t *testing.T) {
	m := &target.Manifest{Name: "grid-10x12", Checksum: "sha256:9f2bc4d1deadbeef"}
	if got := DirName(m); got != "grid-10x12-9f2bc4d1" {
		t.Errorf("DirName = %q, want grid-10x12-9f2bc4d1", got)
	}
	// No checksum yet: the bare name.
	if got := DirName(&target.Manifest{Name: "x"}); got != "x" {
		t.Errorf("DirName without checksum = %q, want x", got)
	}
}

// TestParseHeader covers the typed-header grammar: named property, named id, and
// the bare structural columns.
func TestParseHeader(t *testing.T) {
	cols, err := ParseHeader([]string{"id:ID", "name:STRING", "age:INT", ":LABEL"})
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	want := []target.Column{
		{Name: "id", Type: "ID"},
		{Name: "name", Type: "STRING"},
		{Name: "age", Type: "INT"},
		{Name: "", Type: "LABEL"},
	}
	for i, c := range cols {
		if c != want[i] {
			t.Errorf("col %d = %+v, want %+v", i, c, want[i])
		}
	}
	// FormatHeader is the inverse.
	got := FormatHeader(cols)
	if got[0] != "id:ID" || got[3] != ":LABEL" {
		t.Errorf("FormatHeader = %v", got)
	}
}
