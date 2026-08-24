package gate

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/measure"
)

// A mixed workload's speedup over a rival is two questions wearing one
// number, and averaging them answers neither.
//
// On the read side almost all of the latency is the engine's own work, so
// a ratio there says what the two engines are worth. On the write side a
// durable commit costs one flush of the drive and no engine goes below
// it, so two correct engines on one disk converge no matter how good
// either is, and a mix weighted towards writes drags the combined ratio
// down to the drive. LinkBench is thirty percent durable commits: even a
// write path that costs exactly one sync caps the weighted number well
// under what the read half reaches on its own.
//
// So the comparison is split. Reads are gated as a ratio against the
// rival. Writes are gated in units of one durable sync on the volume the
// run used, which is the part of a commit the engine decides, against the
// rival and against an absolute count.

// DefaultReadSpeedup is how many times faster than the rival the gated
// engine has to answer every read class.
const DefaultReadSpeedup = 10.0

// DefaultCommitSyncs is the most durable syncs a commit may cost at the
// median. One is the floor, since a durable commit has to flush. Two is
// what a writer pays when it arrives an instant after a flush began and
// waits that one out before its own. A median above two is commits that
// are not grouping, which is the engine's to fix and not the drive's.
const DefaultCommitSyncs = 2.0

func (o Options) readSpeedup() float64 {
	if o.ReadSpeedup <= 0 {
		return DefaultReadSpeedup
	}
	return o.ReadSpeedup
}

func (o Options) commitSyncs() float64 {
	if o.CommitSyncs <= 0 {
		return DefaultCommitSyncs
	}
	return o.CommitSyncs
}

// RivalRow is one class of one workload measured on both engines.
type RivalRow struct {
	Class engine.Class

	// P50, P99 are the gated engine's and RivalP50, RivalP99 the rival's.
	P50, P99, RivalP50, RivalP99 time.Duration

	// SpeedupP50 and SpeedupP99 are the rival's latency over the gated
	// engine's, so above one is the gated engine ahead. Zero when either
	// side has no number for the class.
	SpeedupP50, SpeedupP99 float64

	// Syncs and RivalSyncs are the same two latencies in units of one
	// durable sync on the volume each run wrote to, p50 then p99. They
	// are filled for the write class only, and only when both runs owned
	// a store on this machine to probe. A served engine keeps its files
	// somewhere the harness cannot probe and reads zero here.
	Syncs, RivalSyncs [2]float64
}

// Rival is one run compared against a rival's run of the same workload.
type Rival struct {
	// Engine is the gated engine and RivalEngine the one it is held
	// against. Workload and Concurrency are what both of them ran.
	Engine, RivalEngine, Workload string
	Concurrency                   int

	// Rows is one per class both engines produced numbers for, in
	// canonical class order.
	Rows []RivalRow

	// SyncNanos and RivalSyncNanos are what a durable sync probed at on
	// each side, -1 for a run that owned no store to probe. They are
	// printed next to the sync counts because two runs on two volumes
	// are two different units.
	SyncNanos, RivalSyncNanos int64

	// SharedSyncNanos is the one unit both sides were divided by when
	// both runs are on one machine, and zero when they are not. See
	// CompareRival for why it is the cheaper of the two probes.
	SharedSyncNanos int64

	// NotRun are query ids the rival ran and the gated engine did not.
	// They are the reason a class rollup can flatter: a shape an engine
	// cannot express costs it nothing here.
	NotRun []string
}

// classOrder is the order rows read down the page, reads first.
var classOrder = []engine.Class{
	engine.PointRead, engine.Traversal, engine.Subgraph,
	engine.Aggregation, engine.Analytical, engine.Write,
}

// isRead reports whether the speedup gate applies to a class. Analytical
// is out for the same reason it has no budget: it is tracked, never
// gated. Write is out because its latency is mostly the drive's.
func isRead(c engine.Class) bool {
	switch c {
	case engine.PointRead, engine.Traversal, engine.Subgraph, engine.Aggregation:
		return true
	}
	return false
}

// CommitSyncs is a write latency in units of one durable sync on the
// volume it was measured on: its own flush plus every flush it waited
// behind. Zero when the volume was not probed, which is every served
// engine.
func CommitSyncs(latency time.Duration, syncNanos int64) float64 {
	if syncNanos <= 0 || latency <= 0 {
		return 0
	}
	return float64(latency) / float64(syncNanos)
}

// MinDurableSyncNanos is the fastest a flush can come back and still have
// reached a device. Ten microseconds is well under any real drive: a
// laptop's F_FULLFSYNC is milliseconds and an enterprise NVMe with power
// loss protection acknowledges in tens of microseconds.
const MinDurableSyncNanos = int64(10 * time.Microsecond)

// DurableVolume reports whether a probe is a flush that a device
// answered. A volume that does not honour barriers answers instantly and
// the number is a memory copy: WSL2 on a virtual disk probes at 316ns,
// which would turn a 132us commit into four hundred syncs of work and
// fail a write path that is in fact doing nothing wrong. A run there
// says nothing about commits either way, so the write side declines
// rather than deciding.
func DurableVolume(syncNanos int64) bool {
	return syncNanos >= MinDurableSyncNanos
}

// sameHost reports whether two runs were measured on one machine, by the
// part of the stamp a machine cannot change between runs: the chip, how
// many cores of it, how much memory, and the platform.
func sameHost(a, b measure.Hardware) bool {
	return a.CPU == b.CPU && a.Cores == b.Cores && a.RAMBytes == b.RAMBytes &&
		a.OS == b.OS && a.Arch == b.Arch
}

// syncUnit is what the gated engine's write latency was divided by, and
// rivalSyncUnit the same for the rival. They are the shared unit when the
// two runs are on one machine.
func (r Rival) syncUnit() int64 {
	if r.SharedSyncNanos > 0 {
		return r.SharedSyncNanos
	}
	return r.SyncNanos
}

func (r Rival) rivalSyncUnit() int64 {
	if r.SharedSyncNanos > 0 {
		return r.SharedSyncNanos
	}
	return r.RivalSyncNanos
}

// CompareRival builds the comparison. It does not decide anything;
// CheckRival does that and Rival.Write prints it.
func CompareRival(res, rival measure.Result) Rival {
	r := Rival{
		Engine:         res.Condition.Engine,
		RivalEngine:    rival.Condition.Engine,
		Workload:       res.Condition.Workload,
		Concurrency:    peakConcurrency(res.Condition.Concurrency),
		SyncNanos:      res.Condition.Hardware.SyncNanos,
		RivalSyncNanos: rival.Condition.Hardware.SyncNanos,
	}
	// One flush costs one thing on one machine, so when both runs are on
	// one machine both sides are divided by one number. The probe is
	// already a median of fifteen flushes and it still moves: this laptop
	// came back at 3.02ms under one run and 4.35ms under another, a spread
	// wider than the gap between passing the commit ceiling and failing
	// it. Dividing each engine by its own probe would print two columns of
	// one table in two different units and hand whichever engine drew the
	// dearer probe a smaller count for free. The cheaper of the two is the
	// one taken, because it is the least a flush was seen to cost here and
	// because the conservative reading is the one owed to the engine the
	// gate rules on.
	if sameHost(res.Condition.Hardware, rival.Condition.Hardware) &&
		r.SyncNanos > 0 && r.RivalSyncNanos > 0 {
		r.SharedSyncNanos = min(r.SyncNanos, r.RivalSyncNanos)
	}
	for _, class := range classOrder {
		mine, okMine := res.Stats[class]
		theirs, okTheirs := rival.Stats[class]
		if !okMine || !okTheirs || mine.Count == 0 || theirs.Count == 0 {
			continue
		}
		row := RivalRow{
			Class:    class,
			P50:      mine.P50,
			P99:      mine.P99,
			RivalP50: theirs.P50,
			RivalP99: theirs.P99,
		}
		if mine.P50 > 0 {
			row.SpeedupP50 = float64(theirs.P50) / float64(mine.P50)
		}
		if mine.P99 > 0 {
			row.SpeedupP99 = float64(theirs.P99) / float64(mine.P99)
		}
		if class == engine.Write {
			row.Syncs = [2]float64{
				CommitSyncs(mine.P50, r.syncUnit()),
				CommitSyncs(mine.P99, r.syncUnit()),
			}
			row.RivalSyncs = [2]float64{
				CommitSyncs(theirs.P50, r.rivalSyncUnit()),
				CommitSyncs(theirs.P99, r.rivalSyncUnit()),
			}
		}
		r.Rows = append(r.Rows, row)
	}
	for id, stat := range rival.ByQuery {
		if stat.Count == 0 {
			continue
		}
		if mine, ok := res.ByQuery[id]; !ok || mine.Count == 0 {
			r.NotRun = append(r.NotRun, id)
		}
	}
	sort.Strings(r.NotRun)
	return r
}

// CheckRival evaluates the two gates: every read class at or above the
// speedup factor, and the write class no more syncs a commit than the
// rival and no more than the absolute count at the median.
//
// It fails the gated engine and never the rival, which is the same
// contract every other check here keeps: the matrix reports competitors,
// the gate rules on one engine.
func CheckRival(r Rival, opts Options) []Violation {
	want := opts.readSpeedup()
	var out []Violation
	for _, row := range r.Rows {
		if !isRead(row.Class) {
			continue
		}
		check := func(metric string, got float64, mine, theirs time.Duration) {
			if got <= 0 || got >= want {
				return
			}
			out = append(out, Violation{
				Kind:  "rival",
				Where: string(row.Class),
				Detail: fmt.Sprintf("%s %v against %s %v is %.2fx, under the %.2fx read target",
					metric, round(mine), r.RivalEngine, round(theirs), got, want),
			})
		}
		check("p50", row.SpeedupP50, row.P50, row.RivalP50)
		check("p99", row.SpeedupP99, row.P99, row.RivalP99)
	}
	for _, row := range r.Rows {
		if row.Class != engine.Write {
			continue
		}
		if row.Syncs[0] == 0 {
			out = append(out, Violation{
				Kind:  Indeterminate,
				Where: string(engine.Write),
				Detail: fmt.Sprintf("write p50 %v, but the volume was not probed, so this run cannot say what it cost in syncs",
					round(row.P50)),
			})
			continue
		}
		if !DurableVolume(r.syncUnit()) {
			out = append(out, Violation{
				Kind:  Indeterminate,
				Where: string(engine.Write),
				Detail: fmt.Sprintf("write p50 %v on a volume where a flush returns in %v, which is not a flush a device answered, so what the commit cost in syncs is not a question this host can be asked",
					round(row.P50), time.Duration(r.syncUnit())),
			})
			continue
		}
		if ceiling := opts.commitSyncs(); row.Syncs[0] > ceiling {
			out = append(out, Violation{
				Kind:  "rival",
				Where: string(engine.Write),
				Detail: fmt.Sprintf("p50 %v is %.2f durable syncs of %v, over the %.2f a grouped commit costs",
					round(row.P50), row.Syncs[0], time.Duration(r.syncUnit()), ceiling),
			})
		}
		if row.RivalSyncs[0] == 0 {
			continue
		}
		for i, metric := range [2]string{"p50", "p99"} {
			if row.Syncs[i] <= row.RivalSyncs[i] {
				continue
			}
			out = append(out, Violation{
				Kind:  "rival",
				Where: string(engine.Write),
				Detail: fmt.Sprintf("%s costs %.2f syncs a commit against %s at %.2f, so the commit is doing more of the drive's work and not less",
					metric, row.Syncs[i], r.RivalEngine, row.RivalSyncs[i]),
			})
		}
	}
	return out
}

// round trims a latency to something a table can hold. A write measured
// in milliseconds does not need its nanoseconds printed.
func round(d time.Duration) time.Duration {
	switch {
	case d >= time.Millisecond:
		return d.Round(10 * time.Microsecond)
	case d >= time.Microsecond:
		return d.Round(10 * time.Nanosecond)
	}
	return d
}

// Write prints the comparison: one row per class, then the write class
// again in syncs, then what the gated engine did not run. It is the
// table an issue comment quotes, so it prints whether the gate passed or
// not.
func (r Rival) Write(w io.Writer) {
	fmt.Fprintf(w, "rival: %s against %s on %s at a concurrency of %d\n",
		r.Engine, r.RivalEngine, r.Workload, r.Concurrency)
	if len(r.Rows) == 0 {
		fmt.Fprintln(w, "the two runs share no class with numbers on both sides")
	}
	rows := [][]string{{
		"Class",
		r.Engine + " p50", r.Engine + " p99",
		r.RivalEngine + " p50", r.RivalEngine + " p99",
		"p50 x", "p99 x",
	}}
	for _, row := range r.Rows {
		rows = append(rows, []string{
			string(row.Class),
			round(row.P50).String(), round(row.P99).String(),
			round(row.RivalP50).String(), round(row.RivalP99).String(),
			fmt.Sprintf("%.2f", row.SpeedupP50), fmt.Sprintf("%.2f", row.SpeedupP99),
		})
	}
	if len(r.Rows) > 0 {
		writeTable(w, rows)
	}
	for _, row := range r.Rows {
		if row.Class != engine.Write || row.Syncs[0] == 0 {
			continue
		}
		if !DurableVolume(r.syncUnit()) {
			fmt.Fprintf(w, "write in syncs: not asked here, a flush on this volume returns in %v and never reached a device\n",
				time.Duration(r.syncUnit()))
			continue
		}
		fmt.Fprintf(w, "write in durable syncs a commit: %s p50 %.2f, p99 %.2f",
			r.Engine, row.Syncs[0], row.Syncs[1])
		if row.RivalSyncs[0] > 0 {
			fmt.Fprintf(w, "; %s p50 %.2f, p99 %.2f",
				r.RivalEngine, row.RivalSyncs[0], row.RivalSyncs[1])
		}
		if r.SharedSyncNanos > 0 {
			// One machine, one unit, and the number it took is the
			// cheaper of the two probes rather than either engine's own.
			fmt.Fprintf(w, " (one sync %v, the least a flush cost on this machine, both sides)",
				time.Duration(r.SharedSyncNanos))
		} else if row.RivalSyncs[0] > 0 {
			fmt.Fprintf(w, " (one sync %v here against %v there, two machines and so two units)",
				time.Duration(r.SyncNanos), time.Duration(r.RivalSyncNanos))
		} else {
			fmt.Fprintf(w, " (one sync %v)", time.Duration(r.SyncNanos))
		}
		fmt.Fprintln(w)
	}
	if len(r.NotRun) > 0 {
		fmt.Fprintf(w, "%s did not run %d of what %s did: %s. A class rollup missing a shape flatters the engine missing it.\n",
			r.Engine, len(r.NotRun), r.RivalEngine, strings.Join(r.NotRun, ", "))
	}
}

// writeTable prints rows with a rule under the header, columns padded to
// their widest cell.
func writeTable(w io.Writer, rows [][]string) {
	// Widths count runes and not bytes: a microsecond latency prints a
	// two byte mu and a column measured in bytes comes out short by one
	// space for every one of them.
	width := func(s string) int { return utf8.RuneCountInString(s) }
	widths := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			if width(c) > widths[i] {
				widths[i] = width(c)
			}
		}
	}
	for ri, r := range rows {
		for i, c := range r {
			if i > 0 {
				fmt.Fprint(w, "  ")
			}
			fmt.Fprint(w, c+strings.Repeat(" ", widths[i]-width(c)))
		}
		fmt.Fprintln(w)
		if ri == 0 {
			for i, cw := range widths {
				if i > 0 {
					fmt.Fprint(w, "  ")
				}
				fmt.Fprint(w, strings.Repeat("-", cw))
			}
			fmt.Fprintln(w)
		}
	}
}
