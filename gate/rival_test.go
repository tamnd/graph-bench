package gate

import (
	"strings"
	"testing"
	"time"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/measure"
)

// run builds a result the rival checks can read: the classes given, the
// concurrency, and what a durable sync cost on the volume it wrote to.
func rivalRun(name string, sync int64, clients int, stats map[engine.Class]measure.Stat) measure.Result {
	return measure.Result{
		Stats: stats,
		Condition: measure.Condition{
			Engine:      name,
			Workload:    "linkbench",
			Concurrency: []int{clients},
			Hardware:    measure.Hardware{SyncNanos: sync},
		},
	}
}

// TestCompareRivalSplitsTheMix proves the comparison keeps the two halves
// apart: reads come out as a ratio and writes come out in syncs, off the
// same run.
func TestCompareRivalSplitsTheMix(t *testing.T) {
	sync := int64(3 * time.Millisecond)
	mine := rivalRun("zu", sync, 8, map[engine.Class]measure.Stat{
		engine.PointRead: stat(engine.PointRead, 60*time.Microsecond, 260*time.Microsecond),
		engine.Write:     stat(engine.Write, 6*time.Millisecond, 18*time.Millisecond),
	})
	theirs := rivalRun("ladybug", sync, 8, map[engine.Class]measure.Stat{
		engine.PointRead: stat(engine.PointRead, 12*time.Millisecond, 20*time.Millisecond),
		engine.Write:     stat(engine.Write, 13*time.Millisecond, 21*time.Millisecond),
	})
	r := CompareRival(mine, theirs)
	if len(r.Rows) != 2 {
		t.Fatalf("got %d rows, want one per shared class", len(r.Rows))
	}
	if r.Rows[0].Class != engine.PointRead {
		t.Errorf("rows[0] is %s, want reads first", r.Rows[0].Class)
	}
	if got := r.Rows[0].SpeedupP50; got < 199 || got > 201 {
		t.Errorf("point read p50 speedup %.2fx, want 12ms over 60us", got)
	}
	// Two syncs a commit at the median, six at the tail, and the rival's
	// numbers in its own volume's units.
	w := r.Rows[1]
	if w.Syncs != [2]float64{2, 6} {
		t.Errorf("write syncs %v, want 6ms and 18ms over a 3ms sync", w.Syncs)
	}
	if got := w.RivalSyncs[0]; got < 4.3 || got > 4.4 {
		t.Errorf("rival p50 %.2f syncs, want 13ms over a 3ms sync", got)
	}
	// The write row still carries the ratio, because it is worth
	// printing. It is just not what the write side is gated on.
	if w.SpeedupP50 == 0 {
		t.Error("the write row lost its ratio")
	}
}

// TestCompareRivalKeepsOneUnitPerMachine proves the write row is divided
// by one number when both runs are on one machine, and by each run's own
// probe when they are not. The numbers are what this laptop actually
// produced on a paired linkbench run: the same volume probed at 4.13ms
// under zu's run and 3.02ms under ladybug's, and a 7.66ms commit is 1.86
// syncs by the first and 2.53 by the second, which straddles the ceiling.
func TestCompareRivalKeepsOneUnitPerMachine(t *testing.T) {
	writes := func(p50 time.Duration) map[engine.Class]measure.Stat {
		return map[engine.Class]measure.Stat{
			engine.Write: stat(engine.Write, p50, 2*p50),
		}
	}
	mine := rivalRun("zu", int64(4130667), 8, writes(7664*time.Microsecond))
	theirs := rivalRun("ladybug", int64(3024291), 8, writes(13386*time.Microsecond))
	r := CompareRival(mine, theirs)
	if r.SharedSyncNanos != 3024291 {
		t.Fatalf("shared unit %d, want the cheaper of the two probes", r.SharedSyncNanos)
	}
	if got := r.Rows[0].Syncs[0]; got < 2.5 || got > 2.6 {
		t.Errorf("zu p50 %.2f syncs, want 7.66ms over the 3.02ms flush, not over its own 4.13ms", got)
	}
	if v := CheckRival(r, Options{}); len(v) != 1 {
		t.Fatalf("2.53 syncs a commit is over the ceiling of two, got %v", v)
	}

	// Two machines are two units, and the printed form says so rather
	// than pretending the counts are comparable.
	mine.Condition.Hardware.CPU = "Apple M4"
	theirs.Condition.Hardware.CPU = "AMD Ryzen 9"
	r = CompareRival(mine, theirs)
	if r.SharedSyncNanos != 0 {
		t.Errorf("two machines share no unit, got %d", r.SharedSyncNanos)
	}
	if got := r.Rows[0].Syncs[0]; got < 1.8 || got > 1.9 {
		t.Errorf("zu p50 %.2f syncs, want its own volume's 4.13ms flush", got)
	}
	var b strings.Builder
	r.Write(&b)
	if !strings.Contains(b.String(), "two machines and so two units") {
		t.Errorf("the printed form hides that the columns are not comparable:\n%s", b.String())
	}
}

// TestCheckRivalDeclinesACeilingInsideTheProbesBand proves a commit
// count is only ruled on where the volume said the same thing twice.
// Six medians of fifteen flushes on one M4 came back between 3.01 and
// 3.97 ms, and a 7.86 ms commit is 2.61 syncs by the first and 1.98 by
// the last, so the ceiling of two is inside what the host does not
// know about itself.
func TestCheckRivalDeclinesACeilingInsideTheProbesBand(t *testing.T) {
	band := func(low, high int64) measure.Result {
		r := rivalRun("zu", int64(3024291), 8, map[engine.Class]measure.Stat{
			engine.Write: stat(engine.Write, 7860*time.Microsecond, 10*time.Millisecond),
		})
		r.Condition.Hardware.SyncLowNanos = low
		r.Condition.Hardware.SyncHighNanos = high
		return r
	}
	theirs := rivalRun("ladybug", int64(3024291), 8, map[engine.Class]measure.Stat{
		engine.Write: stat(engine.Write, 12*time.Millisecond, 21*time.Millisecond),
	})

	v := CheckRival(CompareRival(band(3010000, 3970000), theirs), Options{})
	if len(v) != 1 || v[0].Kind != Indeterminate {
		t.Fatalf("a ceiling inside the band decides nothing, got %v", v)
	}
	if !strings.Contains(v[0].Detail, "2.61") {
		t.Errorf("the finding does not say how wide the answer is: %q", v[0].Detail)
	}
	if (Decision{Violations: v}).Pass() != true {
		t.Error("an indeterminate finding must not fail the run")
	}

	// A volume that agreed with itself is a volume that can be asked.
	// Both ends of a tight band around three milliseconds put this
	// commit over two syncs, so it fails and says so.
	v = CheckRival(CompareRival(band(2980000, 3060000), theirs), Options{})
	if len(v) != 1 || v[0].Kind != "rival" {
		t.Fatalf("a band entirely over the ceiling is a failure, got %v", v)
	}
}

// TestRivalPrintsWhatTheCountIsWorth proves the band reaches the table
// and not only the gate's findings. A reader quoting one number out of
// a comment should be able to see how much of a number it is.
func TestRivalPrintsWhatTheCountIsWorth(t *testing.T) {
	mine := rivalRun("zu", int64(3024291), 8, map[engine.Class]measure.Stat{
		engine.Write: stat(engine.Write, 7860*time.Microsecond, 10*time.Millisecond),
	})
	mine.Condition.Hardware.SyncLowNanos = 3010000
	mine.Condition.Hardware.SyncHighNanos = 3970000
	theirs := rivalRun("ladybug", int64(3024291), 8, map[engine.Class]measure.Stat{
		engine.Write: stat(engine.Write, 12*time.Millisecond, 21*time.Millisecond),
	})
	var b strings.Builder
	CompareRival(mine, theirs).Write(&b)
	if !strings.Contains(b.String(), "at the dearest flush this volume gave") {
		t.Errorf("the table prints a count and not what it is worth:\n%s", b.String())
	}
}

// TestCheckRivalGatesReadsOnTheRatio proves a read class under the factor
// fails and one over it does not, and that the failure names both
// latencies rather than only the ratio.
func TestCheckRivalGatesReadsOnTheRatio(t *testing.T) {
	sync := int64(3 * time.Millisecond)
	reads := func(p50 time.Duration) Rival {
		return CompareRival(
			rivalRun("zu", sync, 8, map[engine.Class]measure.Stat{
				engine.Traversal: stat(engine.Traversal, p50, p50),
			}),
			rivalRun("ladybug", sync, 8, map[engine.Class]measure.Stat{
				engine.Traversal: stat(engine.Traversal, 10*time.Millisecond, 10*time.Millisecond),
			}),
		)
	}
	if v := CheckRival(reads(500*time.Microsecond), Options{}); len(v) != 0 {
		t.Errorf("20x is over the target, got %v", v)
	}
	v := CheckRival(reads(2*time.Millisecond), Options{})
	if len(v) != 2 {
		t.Fatalf("5x fails on both statistics, got %v", v)
	}
	if !strings.Contains(v[0].Detail, "ladybug 10ms") {
		t.Errorf("the detail does not name what it lost to: %q", v[0].Detail)
	}
	// And the target is a knob, because 10x against one rival is not the
	// same claim as 10x against another.
	if v := CheckRival(reads(2*time.Millisecond), Options{ReadSpeedup: 4}); len(v) != 0 {
		t.Errorf("5x passes a 4x target, got %v", v)
	}
}

// TestCheckRivalGatesWritesInSyncs proves the write side is decided in
// syncs and not in milliseconds: the same 9ms p50 passes on a disk where
// a sync is 4.5ms and fails on one where it is 3ms, because on the fast
// disk it is three flushes of work and on the slow one it is two.
func TestCheckRivalGatesWritesInSyncs(t *testing.T) {
	at := func(sync time.Duration) []Violation {
		return CheckRival(CompareRival(
			rivalRun("zu", int64(sync), 8, map[engine.Class]measure.Stat{
				engine.Write: stat(engine.Write, 9*time.Millisecond, 20*time.Millisecond),
			}),
			rivalRun("ladybug", int64(sync), 8, map[engine.Class]measure.Stat{
				engine.Write: stat(engine.Write, 30*time.Millisecond, 60*time.Millisecond),
			}),
		), Options{})
	}
	if v := at(4500 * time.Microsecond); len(v) != 0 {
		t.Errorf("two syncs a commit is the floor a grouped commit reaches, got %v", v)
	}
	v := at(3 * time.Millisecond)
	if len(v) != 1 {
		t.Fatalf("three syncs a commit is over the ceiling, got %v", v)
	}
	if !strings.Contains(v[0].Detail, "durable syncs") {
		t.Errorf("the detail does not say what the unit is: %q", v[0].Detail)
	}
}

// TestCheckRivalGatesWritesAgainstTheRival proves the second half of the
// write gate: a commit that costs more of the drive than the rival's does
// fails even when it is inside the absolute count, because that is the
// engine doing more of the drive's work and not the drive being slow.
func TestCheckRivalGatesWritesAgainstTheRival(t *testing.T) {
	sync := int64(3 * time.Millisecond)
	v := CheckRival(CompareRival(
		rivalRun("zu", sync, 8, map[engine.Class]measure.Stat{
			engine.Write: stat(engine.Write, 6*time.Millisecond, 30*time.Millisecond),
		}),
		rivalRun("ladybug", sync, 8, map[engine.Class]measure.Stat{
			engine.Write: stat(engine.Write, 6*time.Millisecond, 21*time.Millisecond),
		}),
	), Options{})
	if len(v) != 1 || v[0].Kind != "rival" {
		t.Fatalf("a tail of ten syncs against seven has to fail, got %v", v)
	}
	if !strings.Contains(v[0].Detail, "p99") {
		t.Errorf("the failure does not say which statistic lost: %q", v[0].Detail)
	}
}

// TestCheckRivalDeclinesAnUnprobedVolume proves a served rival, or a run
// this process could not probe, does not turn into a pass or a failure.
// It reports that the question was not answerable.
func TestCheckRivalDeclinesAnUnprobedVolume(t *testing.T) {
	r := CompareRival(
		rivalRun("zu", -1, 8, map[engine.Class]measure.Stat{
			engine.Write: stat(engine.Write, 6*time.Millisecond, 18*time.Millisecond),
		}),
		rivalRun("neo4j", -1, 8, map[engine.Class]measure.Stat{
			engine.Write: stat(engine.Write, 13*time.Millisecond, 21*time.Millisecond),
		}),
	)
	v := CheckRival(r, Options{})
	if len(v) != 1 || v[0].Kind != Indeterminate {
		t.Fatalf("an unprobed volume decides nothing, got %v", v)
	}
	if (Decision{Violations: v}).Pass() != true {
		t.Error("an indeterminate finding must not fail the run")
	}
}

// TestRivalReportsWhatTheGatedEngineSkipped proves a shape the gated
// engine could not run is named. A class rollup that is missing an
// operation is a rollup of a smaller job.
func TestRivalReportsWhatTheGatedEngineSkipped(t *testing.T) {
	mine := rivalRun("zu", -1, 8, nil)
	mine.ByQuery = map[string]measure.Stat{"lb-get-node": {Count: 10}}
	theirs := rivalRun("ladybug", -1, 8, nil)
	theirs.ByQuery = map[string]measure.Stat{
		"lb-get-node":    {Count: 10},
		"lb-delete-node": {Count: 10},
	}
	r := CompareRival(mine, theirs)
	if len(r.NotRun) != 1 || r.NotRun[0] != "lb-delete-node" {
		t.Fatalf("NotRun = %v, want the one shape zu has no text for", r.NotRun)
	}
	var b strings.Builder
	r.Write(&b)
	if !strings.Contains(b.String(), "lb-delete-node") {
		t.Errorf("the printed comparison hides it:\n%s", b.String())
	}
}

// TestRivalWritesATable proves the printed form carries the numbers an
// issue comment quotes: both engines, both statistics, the ratio, and the
// write row restated in syncs.
func TestRivalWritesATable(t *testing.T) {
	sync := int64(3 * time.Millisecond)
	r := CompareRival(
		rivalRun("zu", sync, 8, map[engine.Class]measure.Stat{
			engine.PointRead: stat(engine.PointRead, 60*time.Microsecond, 260*time.Microsecond),
			engine.Write:     stat(engine.Write, 6*time.Millisecond, 18*time.Millisecond),
		}),
		rivalRun("ladybug", sync, 8, map[engine.Class]measure.Stat{
			engine.PointRead: stat(engine.PointRead, 12*time.Millisecond, 20*time.Millisecond),
			engine.Write:     stat(engine.Write, 13*time.Millisecond, 21*time.Millisecond),
		}),
	)
	var b strings.Builder
	r.Write(&b)
	out := b.String()
	for _, want := range []string{
		"zu against ladybug on linkbench at a concurrency of 8",
		"zu p50", "ladybug p99", "point-read", "write",
		"write in durable syncs a commit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the table is missing %q:\n%s", want, out)
		}
	}
}

// TestCheckRivalDeclinesAVolumeThatDoesNotFlush proves a host whose
// flush returns instantly decides nothing. WSL2 on a virtual disk probes
// at 316ns, which would read a healthy 132us commit as four hundred
// syncs of work.
func TestCheckRivalDeclinesAVolumeThatDoesNotFlush(t *testing.T) {
	r := CompareRival(
		rivalRun("zu", 316, 8, map[engine.Class]measure.Stat{
			engine.Write: stat(engine.Write, 132*time.Microsecond, 930*time.Microsecond),
		}),
		rivalRun("ladybug", 319, 8, map[engine.Class]measure.Stat{
			engine.Write: stat(engine.Write, 5*time.Millisecond, 10*time.Millisecond),
		}),
	)
	v := CheckRival(r, Options{})
	if len(v) != 1 || v[0].Kind != Indeterminate {
		t.Fatalf("a volume that does not flush cannot fail a write path, got %v", v)
	}
	var b strings.Builder
	r.Write(&b)
	if !strings.Contains(b.String(), "never reached a device") {
		t.Errorf("the printed form still reads as durable syncs:\n%s", b.String())
	}
}
