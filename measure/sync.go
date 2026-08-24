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

// syncProbeBatches is how many medians of syncProbeRuns flushes the
// banded probe takes.
//
// One median is not enough to say what a flush costs. Six medians of
// fifteen flushes each, taken back to back on one file on an M4, came
// back at 3.91, 3.01, 3.01, 3.18, 3.97 and 3.05 ms: the same probe of
// the same volume a second apart lands at three or at four, and a
// commit count divided by it is off by a third depending on which. The
// band is what the number is, so the probe reports the lowest and the
// highest of its batch medians and lets the gate decide whether the
// question it was asked survives that width.
const syncProbeBatches = 5

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
	nanos, _, _ := DurableSync(dir)
	return nanos
}

// DurableSync measures a durable sync on the volume dir sits on and
// returns what it cost and the band that number sits in: the median of
// every flush it took, then the lowest and the highest of its batch
// medians. All three are -1 when dir is empty or the probe cannot run.
//
// The band is not decoration. A gate that divides a commit by the
// median alone reads a 7.86 ms commit as 2.58 flushes on a probe that
// said three and as 1.97 on a probe of the same volume that said four,
// which is the difference between failing a ceiling of two and clearing
// it. Where the band straddles the answer, the honest report is that
// this host was not asked a question it could answer.
func DurableSync(dir string) (nanos, low, high int64) {
	if dir == "" {
		return -1, -1, -1
	}
	f, err := os.CreateTemp(dir, ".sync-probe-*")
	if err != nil {
		return -1, -1, -1
	}
	defer os.Remove(f.Name())
	defer f.Close()

	page := make([]byte, 4096)
	all := make([]time.Duration, 0, syncProbeRuns*syncProbeBatches)
	medians := make([]time.Duration, 0, syncProbeBatches)
	for range syncProbeBatches {
		took := make([]time.Duration, 0, syncProbeRuns)
		for range syncProbeRuns {
			start := time.Now()
			if _, err := f.WriteAt(page, 0); err != nil {
				return -1, -1, -1
			}
			// os.File.Sync flushes the drive's write cache where the
			// platform has a way to say so, F_FULLFSYNC on darwin, so
			// this is the cost of the promise a database makes and not
			// the cost of reaching the page cache.
			if err := f.Sync(); err != nil {
				return -1, -1, -1
			}
			took = append(took, time.Since(start))
		}
		all = append(all, took...)
		slices.Sort(took)
		medians = append(medians, took[len(took)/2])
	}
	slices.Sort(all)
	slices.Sort(medians)
	return all[len(all)/2].Nanoseconds(),
		medians[0].Nanoseconds(),
		medians[len(medians)-1].Nanoseconds()
}
