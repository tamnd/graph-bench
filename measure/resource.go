package measure

import (
	"io/fs"
	"path/filepath"
	"runtime"
)

// Resource is the cost side of a run beside latency: how much memory the engine
// used and how much disk the data takes. Latency says how fast; Resource says
// at what price, so two engines with the same p99 are not equal if one holds
// the graph in twice the memory.
//
// The memory figures come from the harness process. For an in-process engine
// the harness is the engine, so they describe the engine directly. For a Bolt
// engine the work happens in a separate server process, so these describe only
// the client driver and read near zero; to size a Bolt engine, read the server
// process or the container, not this struct.
type Resource struct {
	// Memory.
	HeapAllocBytes  int64 // Go live heap at the end of the run, measured after a GC
	HeapSysBytes    int64 // heap address space reserved from the OS
	GoSysBytes      int64 // total memory the Go runtime obtained from the OS
	TotalAllocBytes int64 // cumulative bytes allocated during the run (end minus start)
	NumGC           int64 // GC cycles during the run (end minus start)
	GCPauseTotalNs  int64 // total GC stop-the-world pause during the run (end minus start)
	MaxRSSBytes     int64 // process peak resident set, -1 when the platform cannot report it;
	// a process high-water mark since start, so it attributes to one engine
	// cleanly only when a single engine runs per invocation

	// Disk.
	DatasetBytes int64 // materialized dataset directory size on disk, -1 if unknown
	LoadBytes    int64 // engine on-disk footprint after load, -1 for in-memory engines
}

// memSnapshot is a point-in-time reading of the runtime allocator, taken at the
// start and end of an engine's run so the Resource deltas describe that run and
// nothing before it.
type memSnapshot struct {
	heapAlloc    uint64
	heapSys      uint64
	sys          uint64
	totalAlloc   uint64
	numGC        uint64
	pauseTotalNs uint64
}

// SnapshotMem forces a GC so the heap reading reflects live data rather than
// uncollected garbage, then reads the allocator counters. Call it once before
// an engine's Setup and again after its run; hand both to CaptureResource.
func SnapshotMem() memSnapshot {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return memSnapshot{
		heapAlloc:    ms.HeapAlloc,
		heapSys:      ms.HeapSys,
		sys:          ms.Sys,
		totalAlloc:   ms.TotalAlloc,
		numGC:        uint64(ms.NumGC),
		pauseTotalNs: ms.PauseTotalNs,
	}
}

// CaptureResource diffs the end snapshot against the start and attaches the disk
// sizes and the process peak RSS. The counter fields (TotalAlloc, NumGC, pause)
// are deltas describing the work between the snapshots; the heap-in-use fields
// are the end-of-run absolutes, the live footprint the engine settled at.
func CaptureResource(start, end memSnapshot, datasetBytes, loadBytes int64) Resource {
	return Resource{
		HeapAllocBytes:  int64(end.heapAlloc),
		HeapSysBytes:    int64(end.heapSys),
		GoSysBytes:      int64(end.sys),
		TotalAllocBytes: int64(end.totalAlloc - start.totalAlloc),
		NumGC:           int64(end.numGC - start.numGC),
		GCPauseTotalNs:  int64(end.pauseTotalNs - start.pauseTotalNs),
		MaxRSSBytes:     maxRSSBytes(),
		DatasetBytes:    datasetBytes,
		LoadBytes:       loadBytes,
	}
}

// DirSizeBytes returns the total size of all regular files under dir. It returns
// -1 for an empty path or a walk error, so an in-process engine with no
// materialized dataset directory records "unknown" rather than a misleading zero.
func DirSizeBytes(dir string) int64 {
	if dir == "" {
		return -1
	}
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return -1
	}
	return total
}
