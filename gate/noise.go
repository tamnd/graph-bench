package gate

import (
	"fmt"
	"sort"
	"time"

	"github.com/tamnd/graph-bench/measure"
)

// Spread is what repeated runs of one unchanged binary did to one query. It
// is the number a regression factor has to clear to mean anything: if two
// runs of the same code differ by 1.8x, a gate set at 1.10x is reporting the
// machine, not the change.
type Spread struct {
	// Query is the query id the spread was measured over.
	Query string

	// Metric is which latency the spread is over, "p50" or "p99". Both are
	// measured because the gate checks both, and a tail is looser than a
	// median: a floor taken from p50 alone will call p99 noise a
	// regression.
	Metric string

	// Runs is how many results contributed.
	Runs int

	// Min, Median and Max are the metric's values across those runs.
	Min, Median, Max time.Duration

	// Factor is Max/Min, the widest disagreement the machine produced
	// while nothing was changing.
	Factor float64
}

// Noise is a machine's measured run-to-run behaviour over a set of repeats.
type Noise struct {
	// Runs is the number of results compared.
	Runs int

	// Spreads is one entry per query and metric, widest first.
	Spreads []Spread

	// Floor is the suggested Options.NoiseFloor: the Percentile-th
	// percentile of the per-query factors, so one pathological query does
	// not set the bar for the whole suite.
	Floor float64

	// Worst is the widest single factor, reported alongside Floor because
	// the difference between the two is the tail the floor is ignoring.
	Worst float64

	// Percentile is the percentile Floor was taken at, in [0,100].
	Percentile float64
}

// Usable reports whether a gate at this regression factor can say anything on
// this machine. When the floor is at or above the factor, every difference the
// gate could flag is also a difference the machine produces on its own.
func (n Noise) Usable(regressionFactor float64) bool {
	return n.Runs > 1 && n.Floor < regressionFactor
}

// Summary is a one-line verdict on whether the machine can support a gate.
func (n Noise) Summary(regressionFactor float64) string {
	switch {
	case n.Runs < 2:
		return "not enough repeats to measure noise: give it at least two runs of the same binary"
	case n.Usable(regressionFactor):
		return fmt.Sprintf("noise floor %.2fx over %d runs, under the %.2fx gate: this machine can hold the gate",
			n.Floor, n.Runs, regressionFactor)
	default:
		return fmt.Sprintf("noise floor %.2fx over %d runs, at or over the %.2fx gate: this machine cannot tell a regression from itself, so run the matrix somewhere quieter or gate with --noise-floor %.2f",
			n.Floor, n.Runs, regressionFactor, n.Floor)
	}
}

// MeasureNoise compares repeated results for one engine, workload and dataset
// and reports how much they disagreed. The caller is responsible for passing
// runs that differ in nothing but time: same binary, same dataset digest, same
// plane. Queries missing from some runs, or with a zero p50, are skipped
// rather than counted as an infinite spread.
//
// percentile selects the floor from the per-query factors; pass 0 for
// DefaultNoisePercentile.
func MeasureNoise(runs []measure.Result, percentile float64) Noise {
	if percentile <= 0 || percentile > 100 {
		percentile = DefaultNoisePercentile
	}
	n := Noise{Runs: len(runs), Percentile: percentile, Floor: 1, Worst: 1}
	if len(runs) < 2 {
		return n
	}

	// Keyed by query and metric, because the gate judges both and they do
	// not wobble by the same amount.
	type key struct{ query, metric string }
	byQuery := map[key][]time.Duration{}
	for _, r := range runs {
		for id, stat := range r.ByQuery {
			if stat.Count == 0 {
				continue
			}
			for metric, d := range map[string]time.Duration{"p50": stat.P50, "p99": stat.P99} {
				if d <= 0 {
					continue
				}
				k := key{id, metric}
				byQuery[k] = append(byQuery[k], d)
			}
		}
	}

	var factors []float64
	for k, ds := range byQuery {
		if len(ds) < 2 {
			continue
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		s := Spread{
			Query:  k.query,
			Metric: k.metric,
			Runs:   len(ds),
			Min:    ds[0],
			Median: ds[len(ds)/2],
			Max:    ds[len(ds)-1],
			Factor: float64(ds[len(ds)-1]) / float64(ds[0]),
		}
		n.Spreads = append(n.Spreads, s)
		factors = append(factors, s.Factor)
	}
	if len(factors) == 0 {
		return n
	}
	sort.Slice(n.Spreads, func(i, j int) bool {
		if n.Spreads[i].Factor != n.Spreads[j].Factor {
			return n.Spreads[i].Factor > n.Spreads[j].Factor
		}
		if n.Spreads[i].Query != n.Spreads[j].Query {
			return n.Spreads[i].Query < n.Spreads[j].Query
		}
		return n.Spreads[i].Metric < n.Spreads[j].Metric
	})
	sort.Float64s(factors)
	n.Floor = pct(factors, percentile)
	n.Worst = factors[len(factors)-1]
	return n
}

// pct is the nearest-rank percentile of a sorted slice.
func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 1
	}
	rank := int(p/100*float64(len(sorted)) + 0.5)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
