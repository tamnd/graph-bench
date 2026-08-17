package measure

import (
	"io/fs"
	"path/filepath"
	"runtime"
)

// Resource is the cost side of a run beside latency: what the run spent to
// reach the numbers, in memory, CPU, kernel work and disk. Latency says how
// fast; Resource says at what price, so two engines with the same p99 are not
// equal if one holds the graph in twice the memory, burns twice the CPU, or
// pushes ten times the bytes at the disk to make a write durable.
//
// The process figures come from the harness process and the children it has
// reaped. For an in-process engine the harness is the engine, so the self
// fields describe the engine directly. For a subprocess engine the work
// happens in a child, so its cost lands in the Child fields once that child
// has been waited for, which is why the end reading is taken after the session
// is closed. For a Bolt engine the work happens in a server the harness never
// forked, so neither side describes it: to size a Bolt engine, read the server
// process or the container.
//
// A field the platform cannot answer is -1 rather than 0, because a zero that
// means "nothing happened" and a zero that means "nobody asked" are the same
// number and the second one reads like a result.
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
	ChildMaxRSSBytes int64 // peak resident set of the largest reaped child, which is the
	// engine itself on a subprocess plane, -1 when unavailable

	// CPU, as deltas over the run. A latency that is all waiting and a latency
	// that is all computing are the same duration and a different engine.
	CPUUserNs      int64 // user time in the harness process
	CPUSysNs       int64 // system time in the harness process
	ChildCPUUserNs int64 // user time of reaped children, the engine on a subprocess plane
	ChildCPUSysNs  int64 // system time of reaped children

	// Kernel work, as deltas over the run. Major faults and involuntary
	// switches are the two that explain a p99 nothing in the query text can:
	// one is the page cache missing, the other is the scheduler leaving.
	MinorFaults            int64 // page faults served without disk IO
	MajorFaults            int64 // page faults that needed disk IO
	VoluntaryCtxSwitches   int64 // switches where the process waited on something
	InvoluntaryCtxSwitches int64 // switches where the scheduler preempted it
	BlockInputOps          int64 // block input operations, always 0 on darwin
	BlockOutputOps         int64 // block output operations, always 0 on darwin

	// The same kernel work for reaped children, which is where a subprocess
	// engine's own faults and switches are. Without these a subprocess engine
	// reads as faultless, because the faults were the child's.
	ChildMinorFaults            int64
	ChildMajorFaults            int64
	ChildVoluntaryCtxSwitches   int64
	ChildInvoluntaryCtxSwitches int64
	ChildBlockInputOps          int64
	ChildBlockOutputOps         int64

	// Disk.
	DatasetBytes     int64 // materialized dataset directory size on disk, -1 if unknown
	LoadBytes        int64 // engine on-disk footprint after load, -1 for in-memory engines
	StoreBytes       int64 // engine on-disk footprint after the measured run, -1 if unknown
	StoreGrowthBytes int64 // StoreBytes minus LoadBytes, the durable footprint the
	// measured run added, -1 when either end is unknown
	DiskReadBytes int64 // bytes the process fetched from the block layer during the run,
	// -1 where the platform has no per-process counter
	DiskWriteBytes int64 // bytes the process pushed at the block layer during the run,
	// -1 where the platform has no per-process counter
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

// procUsage is a point-in-time reading of what the kernel will say about this
// process and the children it has reaped. ok says whether the rusage read
// worked and ioOK whether the per-process disk byte counters did, because a
// platform can have the first without the second: darwin's getrusage answers
// CPU and faults but reports block operations as zero and has no byte counter
// outside libproc.
type procUsage struct {
	ok               bool
	userNs           int64
	sysNs            int64
	maxRSS           int64
	minorFaults      int64
	majorFaults      int64
	volCtx           int64
	involCtx         int64
	blockIn          int64
	blockOut         int64
	childUserNs      int64
	childSysNs       int64
	childMaxRSS      int64
	childMinorFaults int64
	childMajorFaults int64
	childVolCtx      int64
	childInvolCtx    int64
	childBlockIn     int64
	childBlockOut    int64
	ioOK             bool
	diskReadBytes    int64
	diskWriteBytes   int64
}

// Usage is one reading of the whole process: the Go allocator counters and the
// kernel's accounting, taken together so every delta between two readings
// describes the same interval.
type Usage struct {
	mem  memSnapshot
	proc procUsage
}

// Snapshot forces a GC so the heap reading reflects live data rather than
// uncollected garbage, then reads the allocator counters and the kernel's
// accounting. Call it once before an engine's Start and again after its run
// has finished and its session is closed; hand both to CaptureResource.
//
// Closing first matters on a subprocess plane: a child's CPU and peak resident
// set only appear in the children rusage once it has been waited for, so an
// end reading taken with the engine still running reports the engine as free.
func Snapshot() Usage {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return Usage{
		mem: memSnapshot{
			heapAlloc:    ms.HeapAlloc,
			heapSys:      ms.HeapSys,
			sys:          ms.Sys,
			totalAlloc:   ms.TotalAlloc,
			numGC:        uint64(ms.NumGC),
			pauseTotalNs: ms.PauseTotalNs,
		},
		proc: readProcUsage(),
	}
}

// Disk is the storage side of a capture, measured by the caller because only
// the caller knows where the dataset and the engine's files live. A figure the
// caller cannot answer is -1.
type Disk struct {
	DatasetBytes int64 // materialized dataset directory
	LoadBytes    int64 // engine footprint after load, as the loader reported it
	StoreBytes   int64 // engine footprint after the run, measured after the close
}

// CaptureResource diffs the end reading against the start and attaches the disk
// sizes. The counter fields (allocations, GC, CPU, faults, switches, disk
// bytes) are deltas describing the work between the readings; the heap-in-use
// fields are the end-of-run absolutes, the live footprint the engine settled
// at; the peak resident figures are high-water marks since process start.
func CaptureResource(start, end Usage, disk Disk) Resource {
	r := Resource{
		HeapAllocBytes:  int64(end.mem.heapAlloc),
		HeapSysBytes:    int64(end.mem.heapSys),
		GoSysBytes:      int64(end.mem.sys),
		TotalAllocBytes: int64(end.mem.totalAlloc - start.mem.totalAlloc),
		NumGC:           int64(end.mem.numGC - start.mem.numGC),
		GCPauseTotalNs:  int64(end.mem.pauseTotalNs - start.mem.pauseTotalNs),

		DatasetBytes:     disk.DatasetBytes,
		LoadBytes:        disk.LoadBytes,
		StoreBytes:       disk.StoreBytes,
		StoreGrowthBytes: growth(disk.LoadBytes, disk.StoreBytes),
	}
	if start.proc.ok && end.proc.ok {
		r.MaxRSSBytes = end.proc.maxRSS
		r.ChildMaxRSSBytes = end.proc.childMaxRSS
		r.CPUUserNs = end.proc.userNs - start.proc.userNs
		r.CPUSysNs = end.proc.sysNs - start.proc.sysNs
		r.ChildCPUUserNs = end.proc.childUserNs - start.proc.childUserNs
		r.ChildCPUSysNs = end.proc.childSysNs - start.proc.childSysNs
		r.MinorFaults = end.proc.minorFaults - start.proc.minorFaults
		r.MajorFaults = end.proc.majorFaults - start.proc.majorFaults
		r.VoluntaryCtxSwitches = end.proc.volCtx - start.proc.volCtx
		r.InvoluntaryCtxSwitches = end.proc.involCtx - start.proc.involCtx
		r.BlockInputOps = end.proc.blockIn - start.proc.blockIn
		r.BlockOutputOps = end.proc.blockOut - start.proc.blockOut
		r.ChildMinorFaults = end.proc.childMinorFaults - start.proc.childMinorFaults
		r.ChildMajorFaults = end.proc.childMajorFaults - start.proc.childMajorFaults
		r.ChildVoluntaryCtxSwitches = end.proc.childVolCtx - start.proc.childVolCtx
		r.ChildInvoluntaryCtxSwitches = end.proc.childInvolCtx - start.proc.childInvolCtx
		r.ChildBlockInputOps = end.proc.childBlockIn - start.proc.childBlockIn
		r.ChildBlockOutputOps = end.proc.childBlockOut - start.proc.childBlockOut
	} else {
		r.MaxRSSBytes, r.ChildMaxRSSBytes = -1, -1
		r.CPUUserNs, r.CPUSysNs = -1, -1
		r.ChildCPUUserNs, r.ChildCPUSysNs = -1, -1
		r.MinorFaults, r.MajorFaults = -1, -1
		r.VoluntaryCtxSwitches, r.InvoluntaryCtxSwitches = -1, -1
		r.BlockInputOps, r.BlockOutputOps = -1, -1
		r.ChildMinorFaults, r.ChildMajorFaults = -1, -1
		r.ChildVoluntaryCtxSwitches, r.ChildInvoluntaryCtxSwitches = -1, -1
		r.ChildBlockInputOps, r.ChildBlockOutputOps = -1, -1
	}
	if start.proc.ioOK && end.proc.ioOK {
		r.DiskReadBytes = end.proc.diskReadBytes - start.proc.diskReadBytes
		r.DiskWriteBytes = end.proc.diskWriteBytes - start.proc.diskWriteBytes
	} else {
		r.DiskReadBytes, r.DiskWriteBytes = -1, -1
	}
	return r
}

// growth is the durable footprint the measured run added. Either end unknown
// makes the difference unknown, and a store that shrank (a compaction during
// the run) is reported as the negative number it is rather than clamped.
func growth(loadBytes, storeBytes int64) int64 {
	if loadBytes < 0 || storeBytes < 0 {
		return -1
	}
	return storeBytes - loadBytes
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
