package measure

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// TestCaptureResource checks the reading delta carries through: allocating and
// burning CPU between two readings shows up as a positive TotalAlloc and a
// positive user time, the disk sizes pass through verbatim, store growth is the
// difference, and heap-in-use is a non-negative absolute.
func TestCaptureResource(t *testing.T) {
	start := Snapshot()
	// Allocate something the GC cannot fold away before the end reading, and
	// spend measurable user time doing it so the CPU delta is not zero.
	sink := make([][]byte, 0, 256)
	sum := 0
	for i := 0; i < 256; i++ {
		b := make([]byte, 4096)
		for j := range b {
			b[j] = byte(j)
			sum += int(b[j])
		}
		sink = append(sink, b)
	}
	end := Snapshot()
	r := CaptureResource(start, end, Disk{DatasetBytes: 2048, LoadBytes: 4096, StoreBytes: 5120})
	if r.DatasetBytes != 2048 {
		t.Errorf("DatasetBytes = %d, want 2048", r.DatasetBytes)
	}
	if r.LoadBytes != 4096 {
		t.Errorf("LoadBytes = %d, want 4096", r.LoadBytes)
	}
	if r.StoreBytes != 5120 {
		t.Errorf("StoreBytes = %d, want 5120", r.StoreBytes)
	}
	if r.StoreGrowthBytes != 1024 {
		t.Errorf("StoreGrowthBytes = %d, want 1024", r.StoreGrowthBytes)
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
	if hasRusage() {
		if r.CPUUserNs <= 0 {
			t.Errorf("CPUUserNs = %d, want > 0 after a loop over 1MiB", r.CPUUserNs)
		}
		if r.MinorFaults < 0 {
			t.Errorf("MinorFaults = %d, want >= 0", r.MinorFaults)
		}
		if r.VoluntaryCtxSwitches < 0 || r.InvoluntaryCtxSwitches < 0 {
			t.Errorf("context switches = %d/%d, want >= 0",
				r.VoluntaryCtxSwitches, r.InvoluntaryCtxSwitches)
		}
	}
	runtime.KeepAlive(sink)
	_ = sum
}

// TestGrowthUnknown keeps the -1 convention: a store size nobody could measure
// makes the growth unknown rather than a number that looks like a measurement.
func TestGrowthUnknown(t *testing.T) {
	if got := growth(-1, 4096); got != -1 {
		t.Errorf("growth(-1, 4096) = %d, want -1", got)
	}
	if got := growth(4096, -1); got != -1 {
		t.Errorf("growth(4096, -1) = %d, want -1", got)
	}
	// A store that shrank keeps its sign: a compaction during the run is a
	// result, not an error.
	if got := growth(8192, 4096); got != -4096 {
		t.Errorf("growth(8192, 4096) = %d, want -4096", got)
	}
}

// TestChildUsage checks the children rusage fills in once a child has been
// waited for, which is what makes a subprocess engine's own CPU visible. It
// runs a short sleep and compares the reading before the wait with the one
// after.
func TestChildUsage(t *testing.T) {
	if !hasRusage() {
		t.Skip("no getrusage path on this platform")
	}
	before := Snapshot()
	cmd := exec.Command("sh", "-c", "i=0; while [ $i -lt 200000 ]; do i=$((i+1)); done")
	if err := cmd.Run(); err != nil {
		t.Skipf("child failed: %v", err)
	}
	after := Snapshot()
	r := CaptureResource(before, after, Disk{DatasetBytes: -1, LoadBytes: -1, StoreBytes: -1})
	if r.ChildCPUUserNs+r.ChildCPUSysNs <= 0 {
		t.Errorf("child cpu = %d user + %d sys, want > 0 after a busy child",
			r.ChildCPUUserNs, r.ChildCPUSysNs)
	}
	if r.ChildMaxRSSBytes <= 0 {
		t.Errorf("ChildMaxRSSBytes = %d, want > 0 after a reaped child", r.ChildMaxRSSBytes)
	}
	// The child's faults are the child's, which is the whole reason these
	// fields exist: a shell that started and looped touched pages, and none of
	// them show up in the harness process counters.
	if r.ChildMinorFaults <= 0 {
		t.Errorf("ChildMinorFaults = %d, want > 0 after a reaped child", r.ChildMinorFaults)
	}
	if r.ChildMajorFaults < 0 {
		t.Errorf("ChildMajorFaults = %d, want >= 0", r.ChildMajorFaults)
	}
	if r.StoreGrowthBytes != -1 {
		t.Errorf("StoreGrowthBytes = %d, want -1 when neither end is known", r.StoreGrowthBytes)
	}
}

// hasRusage reports whether this platform has the getrusage path, so a test
// asserting on CPU and faults skips rather than fails where the fields are the
// -1 marker by design.
func hasRusage() bool { return Snapshot().proc.ok }
