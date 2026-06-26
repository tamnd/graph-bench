package ldbc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tamnd/graph-bench/dataset"
)

// TestRepackUpstream runs the deterministic repack against a real extracted LDBC
// datagen tree and checks the result loads back through dataset.Open (which verifies
// the content checksum the repack wrote). It is a manual probe over an out-of-tree
// fixture, skipped unless GR_LDBC_SNAPSHOT names a directory holding an extracted
// composite-merged-fk archive (the one carrying graphs/csv/.../initial_snapshot).
//
// The test copies the tree first so the source stays intact and the repack's in-place
// cleanup has somewhere to write. It then asserts the canonical layout exists, the
// node and edge totals are non-zero, and a second repack of the canonical output is a
// no-op (the manifest short-circuit), the property the in-loader hook relies on so a
// pre-repacked archive is left untouched.
func TestRepackUpstream(t *testing.T) {
	src := os.Getenv("GR_LDBC_SNAPSHOT")
	if src == "" {
		t.Skip("set GR_LDBC_SNAPSHOT to an extracted composite-merged-fk tree")
	}

	// GR_LDBC_OUT keeps the repacked output at a fixed path for downstream manual
	// runs (graph-bench run --dataset-path); otherwise it lands in a temp dir the
	// test framework cleans up.
	dir := os.Getenv("GR_LDBC_OUT")
	if dir == "" {
		dir = t.TempDir()
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}
	if err := copyDir(src, dir); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}

	repacked, err := repackUpstream(dir, "snb-test")
	if err != nil {
		t.Fatalf("repack: %v", err)
	}
	if !repacked {
		t.Fatal("repack reported no-op on a raw upstream tree")
	}

	// The canonical layout: nodes/, rels/, manifest.json, and nothing else left from
	// the upstream tree.
	for _, want := range []string{"manifest.json", "nodes", "rels"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Fatalf("missing %s after repack: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "graphs")); !os.IsNotExist(err) {
		t.Fatalf("upstream graphs/ tree not cleaned up: %v", err)
	}

	// dataset.Open re-reads the manifest and verifies the content checksum, so a clean
	// Open proves the checksum the repack wrote matches the bytes it wrote.
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("open repacked dataset: %v", err)
	}
	m := ds.Manifest()
	if m.NodeCount == 0 || m.EdgeCount == 0 {
		t.Fatalf("empty dataset: %d nodes, %d edges", m.NodeCount, m.EdgeCount)
	}
	t.Logf("repacked: %d nodes, %d edges, checksum %s", m.NodeCount, m.EdgeCount, m.Checksum)

	// Every node label and relationship type the schema names must have a readable
	// typed header, the contract the gr adapter loads from.
	for label := range m.Schema.Nodes {
		if _, _, err := ds.NodeFiles(label); err != nil {
			t.Fatalf("node files for %s: %v", label, err)
		}
	}
	for typ := range m.Schema.Relationships {
		if _, _, err := ds.RelFiles(typ); err != nil {
			t.Fatalf("rel files for %s: %v", typ, err)
		}
	}

	// A second repack of the now-canonical directory is the no-op path.
	again, err := repackUpstream(dir, "snb-test")
	if err != nil {
		t.Fatalf("second repack: %v", err)
	}
	if again {
		t.Fatal("second repack rewrote an already-canonical directory")
	}
}
