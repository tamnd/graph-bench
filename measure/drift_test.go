package measure

import (
	"errors"
	"testing"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// spread builds samples of one class laid out evenly across a span, each with
// the latency the caller's function gives for its position.
func spread(class engine.Class, n int, span time.Duration, lat func(i int) time.Duration) []Sample {
	out := make([]Sample, n)
	for i := range out {
		out[i] = Sample{
			Class:   class,
			QueryID: string(class),
			Start:   time.Duration(int64(span) * int64(i) / int64(n)),
			Latency: lat(i),
		}
	}
	return out
}

// TestDriftOfSteadyRun proves a run whose latency never changes reads 1.00x,
// which is the number a passing gate is looking for.
func TestDriftOfSteadyRun(t *testing.T) {
	s := spread(engine.PointRead, 600, 60*time.Second, func(int) time.Duration { return ms(1) })
	d, ok := DriftOf(s, 10*time.Second)[engine.PointRead]
	if !ok {
		t.Fatal("no drift for a run covering six windows")
	}
	// The last sample starts just short of 60s, so five 10s windows are
	// whole and the rest is the tail that whole-windows-only drops.
	if d.Windows != 5 {
		t.Errorf("Windows = %d, want 5", d.Windows)
	}
	if d.Trend != 1.0 {
		t.Errorf("Trend = %v, want 1.0 for a run that never changed", d.Trend)
	}
	if d.First.P99 != ms(1) || d.Worst.P99 != ms(1) {
		t.Errorf("first/worst p99 = %v/%v, want 1ms/1ms", d.First.P99, d.Worst.P99)
	}
}

// TestDriftOfDegradingRun proves the check catches the case it is for: a run
// that gets steadily slower as it goes. This is what zu's writes do under a
// sustained mix, going from a 10 ms p99 in the first window to 18 ms in the
// eighth, and the one p99 the run publishes averages the fast opening in and
// says nothing about it.
func TestDriftOfDegradingRun(t *testing.T) {
	// One millisecond per window, climbing.
	s := spread(engine.Write, 800, 80*time.Second, func(i int) time.Duration {
		return ms(1 + i/100)
	})
	d := DriftOf(s, 10*time.Second)[engine.Write]
	if d.First.P99 != ms(1) {
		t.Errorf("first window p99 = %v, want 1ms", d.First.P99)
	}
	if d.Worst.P99 != ms(7) {
		t.Errorf("worst window p99 = %v, want 7ms", d.Worst.P99)
	}
	if d.WorstAt != 60*time.Second {
		t.Errorf("WorstAt = %v, the slowest window is the last one", d.WorstAt)
	}
	// Seven whole windows at 1ms through 7ms: the middle one goes to
	// neither half, so it is the median of 5, 6, 7 over 1, 2, 3.
	if d.Trend != 3.0 {
		t.Errorf("Trend = %v, want 3.0", d.Trend)
	}
}

// TestDriftOfAnEarlyStep proves what the trend does with a run that degrades
// at once and then holds: the second half is slower than the first, but by
// less than the step, because the first half contains the slow part too.
// That is the honest reading of a half-against-half comparison and the
// reason First and Worst are reported beside it.
func TestDriftOfAnEarlyStep(t *testing.T) {
	s := spread(engine.Write, 800, 80*time.Second, func(i int) time.Duration {
		if i < 100 {
			return ms(1)
		}
		return ms(4)
	})
	d := DriftOf(s, 10*time.Second)[engine.Write]
	if d.First.P99 != ms(1) || d.Worst.P99 != ms(4) {
		t.Errorf("first/worst p99 = %v/%v, want 1ms/4ms", d.First.P99, d.Worst.P99)
	}
	if d.Trend != 1.0 {
		t.Errorf("Trend = %v, want 1.0: both halves are the slow number", d.Trend)
	}
}

// TestDriftOfShortRun proves a run too short to hold two whole windows
// reports nothing rather than comparing a window against a fragment.
func TestDriftOfShortRun(t *testing.T) {
	for _, span := range []time.Duration{0, 5 * time.Second, 19 * time.Second} {
		s := spread(engine.PointRead, 100, span, func(int) time.Duration { return ms(1) })
		if d := DriftOf(s, 10*time.Second); d != nil {
			t.Errorf("a %v run reported drift over 10s windows: %v", span, d)
		}
	}
}

// TestDriftOfDropsAnAbsentClass proves a class the schedule left out of some
// window is dropped rather than compared against an empty one. A rare write
// that fires in the first window and not the third has no p99 for the third
// and a zero there would read as an improvement.
func TestDriftOfDropsAnAbsentClass(t *testing.T) {
	s := spread(engine.PointRead, 600, 60*time.Second, func(int) time.Duration { return ms(1) })
	// One write, in the opening window only.
	s = append(s, Sample{Class: engine.Write, Start: time.Second, Latency: ms(9)})
	d := DriftOf(s, 10*time.Second)
	if _, ok := d[engine.Write]; ok {
		t.Error("a class present in one window of six was compared across them")
	}
	if _, ok := d[engine.PointRead]; !ok {
		t.Error("dropping the write dropped the reads with it")
	}
}

// TestDriftOfErrorsExcluded proves the per-window stats follow the same rule
// the run's own stats do: an error counts but its latency does not.
func TestDriftOfErrorsExcluded(t *testing.T) {
	s := spread(engine.Traversal, 600, 60*time.Second, func(int) time.Duration { return ms(1) })
	for i := range s {
		if i%100 == 50 {
			s[i].Latency = time.Second
			s[i].Err = errors.New("timeout")
		}
	}
	d := DriftOf(s, 10*time.Second)[engine.Traversal]
	if d.Worst.P99 > ms(2) {
		t.Errorf("worst window p99 = %v, an error latency leaked into a window", d.Worst.P99)
	}
	if d.Worst.Errors == 0 {
		t.Error("the errors were dropped instead of counted")
	}
}

// TestDriftOfDefaultWindow proves a zero window means DefaultDriftWindow
// rather than dividing by zero.
func TestDriftOfDefaultWindow(t *testing.T) {
	s := spread(engine.PointRead, 600, 60*time.Second, func(int) time.Duration { return ms(1) })
	d := DriftOf(s, 0)[engine.PointRead]
	if d.Window != DefaultDriftWindow {
		t.Errorf("Window = %v, want the default %v", d.Window, DefaultDriftWindow)
	}
}

// TestDriftTrendIgnoresOneBadWindow proves the gated number is a trend and
// not a maximum. A run that wobbles once and is otherwise flat did not
// degrade, and a check that read the worst window would fail it, and would
// fail it harder the longer the run went since the worst of more windows is
// worse for no reason but the counting.
func TestDriftTrendIgnoresOneBadWindow(t *testing.T) {
	s := spread(engine.PointRead, 800, 80*time.Second, func(i int) time.Duration {
		// One window in the middle is four times slower.
		if i >= 400 && i < 500 {
			return ms(4)
		}
		return ms(1)
	})
	d := DriftOf(s, 10*time.Second)[engine.PointRead]
	if d.Worst.P99 != ms(4) {
		t.Fatalf("worst window p99 = %v, want the 4ms one", d.Worst.P99)
	}
	if d.Trend != 1.0 {
		t.Errorf("Trend = %v, want 1.0: one bad window in eight is not a trend", d.Trend)
	}
}

// TestDriftTrendOnAnOddWindowCount proves the middle window goes to neither
// half, so the two sides being compared are the same size.
func TestDriftTrendOnAnOddWindowCount(t *testing.T) {
	if got := trendOf([]time.Duration{ms(1), ms(1), ms(99), ms(2), ms(2)}); got != 2.0 {
		t.Errorf("trendOf = %v, want 2.0 with the middle window left out", got)
	}
	if got := trendOf([]time.Duration{ms(1)}); got != 0 {
		t.Errorf("trendOf of one window = %v, want 0: nothing to compare", got)
	}
}
