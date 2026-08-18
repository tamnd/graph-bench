package gate

import (
	"fmt"
	"sort"
	"time"

	"github.com/tamnd/graph-bench/measure"
)

// DefaultPairedMetric is the statistic PairedAB compares when the caller does
// not name one. It is the per-run minimum, which is the fastest the engine was
// observed to answer, because that is the statistic a busy machine cannot
// inflate: load only ever adds time to a call, so the smallest observation is
// the one closest to the work itself.
const DefaultPairedMetric = "min"

// PairedMetrics are the statistics PairedAB knows how to compare.
var PairedMetrics = []string{"min", "p50", "p99"}

// Pair is one query and one metric measured on both sides of a change.
type Pair struct {
	// Query is the query id, and Workload the workload it belongs to, so
	// two workloads that name a query the same are still two rows.
	Workload, Query string

	// Metric is which latency this row compares: "min", "p50" or "p99".
	Metric string

	// Before and After are the best value each side reached over its
	// repeats.
	Before, After time.Duration

	// BeforeRuns and AfterRuns are how many repeats each side had. They
	// belong next to the numbers because best-of-N gets better with N, so
	// a row where one side ran more often is a row that flatters the
	// other one.
	BeforeRuns, AfterRuns int

	// Factor is After/Before. Above one is slower after the change.
	Factor float64
}

// String renders one row the way the command prints it.
func (p Pair) String() string {
	return fmt.Sprintf("%s/%s %s: %v to %v (%.2fx, best of %d and %d)",
		p.Workload, p.Query, p.Metric, p.Before, p.After, p.Factor, p.BeforeRuns, p.AfterRuns)
}

// Paired is what two sets of repeats said about one change.
type Paired struct {
	// Metric is the statistic that was compared.
	Metric string

	// Pairs is one row per query, slowest first.
	Pairs []Pair

	// Median is the middle Factor, the change's typical effect, and Worst
	// and Best are the ends of the range.
	Median, Worst, Best float64

	// Threshold is the factor a row has to reach to count as regressed.
	Threshold float64
}

// Regressed returns the rows at or over the threshold, slowest first.
func (p Paired) Regressed() []Pair {
	var out []Pair
	for _, pair := range p.Pairs {
		if pair.Factor >= p.Threshold {
			out = append(out, pair)
		}
	}
	return out
}

// Pass reports whether nothing regressed.
func (p Paired) Pass() bool { return len(p.Regressed()) == 0 }

// Summary is a one line verdict.
func (p Paired) Summary() string {
	if len(p.Pairs) == 0 {
		return "no query ran on both sides: check that the two lineages hold the same engine, workload and scale"
	}
	reg := p.Regressed()
	if len(reg) == 0 {
		return fmt.Sprintf("%d quer(ies) compared on %s, median %.2fx, worst %.2fx, nothing at or over %.2fx",
			len(p.Pairs), p.Metric, p.Median, p.Worst, p.Threshold)
	}
	return fmt.Sprintf("%d quer(ies) compared on %s, median %.2fx, %d at or over %.2fx, worst %s at %.2fx",
		len(p.Pairs), p.Metric, p.Median, len(reg), p.Threshold, reg[0].Query, reg[0].Factor)
}

// PairedAB compares two builds of one engine that were run against each other
// on the same machine, and reports what changed per query.
//
// It exists because a stored baseline answers a different question. The gate
// compares today's run against a run recorded on some other day, which is
// sound while the machine is the same machine, and stops being sound the
// moment something else is compiling on it: every query drifts by the same
// tenth and the gate reads the load as a regression in code that never
// touched the read path. A paired run does not have that problem, because
// both sides carry whatever the machine was doing.
//
// The caller owns two things. The first is that the two lineages differ in
// the change and in nothing else: same harness binary, same workload, same
// scale, same dataset. The second is the ordering. Run the two sides
// alternately and swap which one goes first every round, because load that
// climbs through a round lands entirely on whichever side runs second, and a
// counterbalanced order is what stops that from becoming the answer.
//
// The comparison is best-of-N per side on the chosen metric. Rows a side
// never ran, or ran with no observations, are dropped rather than counted as
// infinitely slow.
func PairedAB(before, after map[string][]measure.Result, metric string, threshold float64) Paired {
	if metric == "" {
		metric = DefaultPairedMetric
	}
	if threshold <= 0 {
		threshold = DefaultRegressionFactor
	}
	p := Paired{Metric: metric, Threshold: threshold, Median: 1, Worst: 1, Best: 1}

	for workload, beforeRuns := range before {
		afterRuns, ok := after[workload]
		if !ok {
			continue
		}
		bestBefore, countBefore := bestPerQuery(beforeRuns, metric)
		bestAfter, countAfter := bestPerQuery(afterRuns, metric)
		for query, b := range bestBefore {
			a, ok := bestAfter[query]
			if !ok || b <= 0 || a <= 0 {
				continue
			}
			p.Pairs = append(p.Pairs, Pair{
				Workload:   workload,
				Query:      query,
				Metric:     metric,
				Before:     b,
				After:      a,
				BeforeRuns: countBefore[query],
				AfterRuns:  countAfter[query],
				Factor:     float64(a) / float64(b),
			})
		}
	}
	if len(p.Pairs) == 0 {
		return p
	}

	sort.Slice(p.Pairs, func(i, j int) bool {
		if p.Pairs[i].Factor != p.Pairs[j].Factor {
			return p.Pairs[i].Factor > p.Pairs[j].Factor
		}
		if p.Pairs[i].Workload != p.Pairs[j].Workload {
			return p.Pairs[i].Workload < p.Pairs[j].Workload
		}
		return p.Pairs[i].Query < p.Pairs[j].Query
	})
	factors := make([]float64, 0, len(p.Pairs))
	for _, pair := range p.Pairs {
		factors = append(factors, pair.Factor)
	}
	sort.Float64s(factors)
	p.Median = factors[len(factors)/2]
	p.Best = factors[0]
	p.Worst = factors[len(factors)-1]
	return p
}

// bestPerQuery reduces one side's repeats to the best value each query
// reached, and how many repeats contributed to it.
func bestPerQuery(runs []measure.Result, metric string) (map[string]time.Duration, map[string]int) {
	best := map[string]time.Duration{}
	count := map[string]int{}
	for _, r := range runs {
		for id, stat := range r.ByQuery {
			if stat.Count == 0 {
				continue
			}
			d := pairedMetric(stat, metric)
			if d <= 0 {
				continue
			}
			count[id]++
			if cur, ok := best[id]; !ok || d < cur {
				best[id] = d
			}
		}
	}
	return best, count
}

// pairedMetric picks one statistic off a query's numbers.
func pairedMetric(stat measure.Stat, metric string) time.Duration {
	switch metric {
	case "p50":
		return stat.P50
	case "p99":
		return stat.P99
	default:
		return stat.Min
	}
}
