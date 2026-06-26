package measure

import "time"

// TEPS is the Graph500 traversal-rate metric: traversed edges per second, the
// number of edges a breadth-first traversal examined divided by the time it took
// (doc 06, the Graph500 kernel in doc 05 section 8.3). It is reported beside the
// latency for the graph500 workload, the way Graph500 itself reports a single
// rate number for a BFS kernel. A non-positive duration returns zero rather than
// an infinity, so a mis-timed run shows an obviously empty rate instead of a
// poisoned one.
func TEPS(edgesTraversed int64, d time.Duration) float64 {
	if d <= 0 || edgesTraversed <= 0 {
		return 0
	}
	return float64(edgesTraversed) / d.Seconds()
}

// HarmonicMeanTEPS combines the TEPS of several BFS runs the way Graph500
// aggregates its 64 source samples: the harmonic mean of the per-run rates, which
// is the rate-correct average (the arithmetic mean of rates overweights the fast
// runs). Runs with a non-positive rate are skipped; an empty or all-zero input
// returns zero.
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
