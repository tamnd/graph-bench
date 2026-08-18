package measure

import "time"

// TEPS is the Graph500 traversal-rate metric (spec 08 §1 metric 6): traversed
// edges per second, the number of edges a breadth-first traversal examined
// divided by the time it took. It is reported beside the latency for the g500
// workload, the way Graph500 itself reports a single rate number for a BFS
// kernel. A non-positive duration returns zero rather than an infinity, so a
// mis-timed run shows an obviously empty rate instead of a poisoned one.
func TEPS(edgesTraversed int64, d time.Duration) float64 {
	if d <= 0 || edgesTraversed <= 0 {
		return 0
	}
	return float64(edgesTraversed) / d.Seconds()
}

// HarmonicMeanTEPS combines the TEPS of several BFS runs the way Graph500
// aggregates its 64 source samples: the harmonic mean of the per-run rates,
// which is the rate-correct average (the arithmetic mean of rates overweights
// the fast runs). Runs with a non-positive rate are skipped; an empty or
// all-zero input returns zero.
func HarmonicMeanTEPS(rates []float64) float64 {
	var sumRecip float64
	var n int
	for _, r := range rates {
		if r > 0 {
			sumRecip += 1 / r
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return float64(n) / sumRecip
}

// Traversal is what one kernel's repetitions reached, the TEPS section of a
// result: the source they ran from, the edge work a full traversal from it
// does, the rate each timed repetition reached, and their harmonic mean.
//
// The rate is the same number Graph500 headlines, computed the same way. It
// is per repetition rather than per root because the analytics protocol
// draws one source per query per run: what varies here is the machine, not
// the graph, and the spread across repetitions is what says whether the rate
// is worth quoting.
type Traversal struct {
	Source       string
	Edges        int64
	PerRep       []float64
	HarmonicMean float64
}

// NewTraversal builds the section for one kernel from the edge work and the
// kept repetition durations. A rep that took no measurable time contributes
// no rate, the same rule TEPS itself follows, and a kernel with no edges to
// traverse comes back zeroed rather than as an infinity.
func NewTraversal(source string, edges int64, durs []time.Duration) Traversal {
	t := Traversal{Source: source, Edges: edges}
	for _, d := range durs {
		t.PerRep = append(t.PerRep, TEPS(edges, d))
	}
	t.HarmonicMean = HarmonicMeanTEPS(t.PerRep)
	return t
}
