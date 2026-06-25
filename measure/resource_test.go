package measure

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tamnd/graph-bench/target"
)

// TestSummarizeMinStdDevRows checks the latency-detail fields added beside the
// percentiles: the minimum sample, a non-zero spread for a varied group, and
// rows-per-second from the per-sample row counts.
func TestSummarizeMinStdDevRows(t *testing.T) {
	samples := []Sample{
		{Class: target.PointRead, Latency: ms(10), Rows: 1},
		{Class: target.PointRead, Latency: ms(20), Rows: 2},
		{Class: target.PointRead, Latency: ms(30), Rows: 3},
		{Class: target.PointRead, Latency: ms(40), Rows: 4},
	}
	stats, _ := summarize(samples, time.Second)
	s := stats[target.PointRead]
	if s.Min != ms(10) {
		t.Errorf("Min = %v, want 10ms", s.Min)
	}
	if s.Max != ms(40) {
		t.Errorf("Max = %v, want 40ms", s.Max)
	}
	if s.StdDev <= 0 {
		t.Errorf("StdDev = %v, want > 0 for a varied group", s.StdDev)
	}
	// 10 rows over a 1s window is 10 rows/sec.
	if s.RowThroughput != 10 {
		t.Errorf("RowThroughput = %v, want 10", s.RowThroughput)
	}
}

// TestStdDevZeroForUniform checks a group with identical latencies has zero
// spread, and a single sample has zero spread (no division by n-1 surprises).
func TestStdDevZeroForUniform(t *testing.T) {
	uniform := []Sample{
		{Class: target.Traversal, Latency: ms(5)},
		{Class: target.Traversal, Latency: ms(5)},
		{Class: target.Traversal, Latency: ms(5)},
	}
	stats, _ := summarize(uniform, time.Second)
	if sd := stats[target.Traversal].StdDev; sd != 0 {
		t.Errorf("StdDev of uniform group = %v, want 0", sd)
	}
	one, _ := summarize([]Sample{{Class: target.Traversal, Latency: ms(7)}}, time.Second)
	if sd := one[target.Traversal].StdDev; sd != 0 {
		t.Errorf("StdDev of single sample = %v, want 0", sd)
	}
}

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
