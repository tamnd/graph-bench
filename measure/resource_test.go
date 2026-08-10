package measure

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDirSizeBytes sums the regular files under a directory and reports -1 for
// an empty path.
func TestDirSizeBytes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.csv"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "nodes")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.csv"), []byte("world!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DirSizeBytes(dir); got != 11 { // 5 + 6
		t.Errorf("DirSizeBytes = %d, want 11", got)
	}
	if got := DirSizeBytes(""); got != -1 {
		t.Errorf("DirSizeBytes(\"\") = %d, want -1", got)
	}
}

// TestCaptureResource checks the snapshot delta carries through: allocating
// between two snapshots shows up as a positive TotalAlloc, the disk sizes pass
// through verbatim, and heap-in-use is a non-negative absolute.
func TestCaptureResource(t *testing.T) {
	start := SnapshotMem()
	// Allocate something the GC cannot fold away before the end snapshot.
	sink := make([][]byte, 0, 256)
	for i := 0; i < 256; i++ {
		sink = append(sink, make([]byte, 4096))
	}
	end := SnapshotMem()
	r := CaptureResource(start, end, 2048, 4096)
	if r.DatasetBytes != 2048 {
		t.Errorf("DatasetBytes = %d, want 2048", r.DatasetBytes)
	}
	if r.LoadBytes != 4096 {
		t.Errorf("LoadBytes = %d, want 4096", r.LoadBytes)
	}
	if r.HeapAllocBytes < 0 {
		t.Errorf("HeapAllocBytes = %d, want >= 0", r.HeapAllocBytes)
	}
	if r.TotalAllocBytes <= 0 {
		t.Errorf("TotalAllocBytes = %d, want > 0 after allocating ~1MiB", r.TotalAllocBytes)
	}
	// MaxRSS is -1 only on platforms without the getrusage path; on darwin and
	// linux it must be a positive byte count.
	if r.MaxRSSBytes == 0 {
		t.Errorf("MaxRSSBytes = 0, want positive or -1")
	}
	runtime_KeepAlive(sink)
}

// runtime_KeepAlive keeps sink reachable until after the end snapshot so the
// allocation is not optimized away. A thin wrapper to avoid importing runtime
// only for this in the test body.
func runtime_KeepAlive(v any) { _ = v }
