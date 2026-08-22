package measure

import (
	"os"
	"slices"
	"time"
)

// syncProbeRuns is how many times DurableSyncNanos flushes. Enough for a
// median that is not one scheduling accident, few enough that the probe
// costs a fraction of a second on a disk where a flush is milliseconds.
const syncProbeRuns = 15

// DurableSyncNanos measures what one durable sync costs on the volume dir
// sits on: the median of writing a page and flushing it through the
// drive's own cache. It returns -1 when dir is empty or the probe cannot
// run, which is the same "unreadable" convention the rest of the Hardware
// stamp uses.
//
// This is the floor under every write number a benchmark reports. An
// engine that commits durably pays at least one of these per transaction,
// and no engine-side work goes below it, so a run whose write latency
// equals this number is a run that measured the disk. On this laptop it
// is about 3 ms, which is above the 2 ms write budget the spec table
// carries, and that is why the budget is calibrated against it rather
// than read off the table.
//
// The probe writes to a file of its own inside dir and removes it, so a
// store size taken afterwards is the store's.
func DurableSyncNanos(dir string) int64 {
	if dir == "" {
		return -1
	}
	f, err := os.CreateTemp(dir, ".sync-probe-*")
	if err != nil {
		return -1
	}
	defer os.Remove(f.Name())
	defer f.Close()

	page := make([]byte, 4096)
	took := make([]time.Duration, 0, syncProbeRuns)
	for range syncProbeRuns {
		start := time.Now()
		if _, err := f.WriteAt(page, 0); err != nil {
			return -1
		}
		// os.File.Sync flushes the drive's write cache where the
		// platform has a way to say so, F_FULLFSYNC on darwin, so this
		// is the cost of the promise a database makes and not the cost
		// of reaching the page cache.
		if err := f.Sync(); err != nil {
			return -1
		}
		took = append(took, time.Since(start))
	}
	slices.Sort(took)
	return took[len(took)/2].Nanoseconds()
}
