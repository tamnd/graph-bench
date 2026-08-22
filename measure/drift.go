package measure

import (
	"slices"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// DefaultDriftWindow is how long a window is when the caller does not say.
// Ten seconds is long enough that one slow query does not move a p99 and
// short enough that a minute of running is six of them.
const DefaultDriftWindow = 10 * time.Second

// Drift is what a class's latency did over the length of a run. A run that
// is fast for ten seconds and then slower for fifty is a run whose headline
// number is a lie, and the single p99 over the whole run cannot tell the two
// apart: a store that grows a compaction backlog, a cache that fills, a free
// list that fragments all report a fine p99 for a while.
//
// Trend is the number to gate on: the median window p99 of the run's second
// half over its first, so 1.0 is a run that ended the way it started. It is
// a median of halves rather than the worst window over the first because the
// worst of eight windows beats the first one even on a run that never
// changed, being the largest of eight draws, and a check built on that gets
// stricter every time the run gets longer.
//
// First, Worst and WorstAt are for reading rather than gating: they say how
// bad it got and when, which a trend does not.
type Drift struct {
	Window  time.Duration
	Windows int
	First   Stat
	Worst   Stat
	WorstAt time.Duration // when the worst window opened, from the run's start
	Trend   float64

	// P99s is every window's p99 in order. Every number above is a summary
	// of it, and the series is what says whether a run drifted or wobbled.
	P99s []time.Duration
}

// DriftOf splits samples into fixed windows by when each one started and
// reports, per class, how the p99 of the worst window compares with the
// first. It returns nil when the samples do not cover two whole windows,
// because one window has nothing to drift against and a partial last window
// holds too few samples for a p99 to mean anything.
//
// A window of zero means DefaultDriftWindow.
func DriftOf(samples []Sample, window time.Duration) map[engine.Class]Drift {
	if window <= 0 {
		window = DefaultDriftWindow
	}
	var span time.Duration
	for _, s := range samples {
		if s.Start > span {
			span = s.Start
		}
	}
	windows := int(span / window)
	if windows < 2 {
		return nil
	}

	// Only whole windows count. The tail beyond the last whole one is
	// dropped rather than compared, since a window that got a tenth of
	// the traffic answers with its sample count and not its latency.
	buckets := map[engine.Class][][]Sample{}
	for _, s := range samples {
		w := int(s.Start / window)
		if w >= windows {
			continue
		}
		b, ok := buckets[s.Class]
		if !ok {
			b = make([][]Sample, windows)
		}
		b[w] = append(b[w], s)
		buckets[s.Class] = b
	}

	out := map[engine.Class]Drift{}
	for class, b := range buckets {
		stats := make([]Stat, 0, windows)
		for _, win := range b {
			// A class the schedule left out of some window cannot be
			// compared across them, so the whole class is dropped
			// rather than compared against an empty window.
			if len(win) == 0 {
				stats = nil
				break
			}
			stats = append(stats, summarizeGroup(class, win, window))
		}
		if len(stats) < 2 {
			continue
		}
		d := Drift{Window: window, Windows: len(stats), First: stats[0], Worst: stats[0]}
		for i, s := range stats {
			d.P99s = append(d.P99s, s.P99)
			if s.P99 > d.Worst.P99 {
				d.Worst = s
				d.WorstAt = time.Duration(i) * window
			}
		}
		d.Trend = trendOf(d.P99s)
		out[class] = d
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// trendOf is the median of the second half of a series over the median of
// the first. An odd number of windows leaves the middle one out of both
// halves rather than giving it to one of them, so the two sides are the same
// size and the number is a comparison and not an accident of parity.
//
// It returns 0 for a series too short or a first half of zeroes, which the
// caller reads as nothing to say.
func trendOf(p99s []time.Duration) float64 {
	half := len(p99s) / 2
	if half == 0 {
		return 0
	}
	first := medianOf(p99s[:half])
	second := medianOf(p99s[len(p99s)-half:])
	if first <= 0 {
		return 0
	}
	return float64(second) / float64(first)
}

func medianOf(in []time.Duration) time.Duration {
	s := slices.Clone(in)
	slices.Sort(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}
