package gate

import (
	"testing"
	"time"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/measure"
)

// pairedRun builds one result from a query id to minimum latency map, with a
// p50 and a p99 above it so the metric switch has something to switch on.
func pairedRun(mins map[string]time.Duration) measure.Result {
	byQuery := map[string]measure.Stat{}
	for id, d := range mins {
		byQuery[id] = measure.Stat{
			Class: engine.PointRead,
			Count: 100,
			Min:   d,
			P50:   d * 2,
			P99:   d * 5,
		}
	}
	return measure.Result{ByQuery: byQuery}
}

func TestPairedABTakesTheBestOfEachSide(t *testing.T) {
	us := time.Microsecond
	before := map[string][]measure.Result{"micro-read": {
		pairedRun(map[string]time.Duration{"point": 10 * us, "scan": 40 * us}),
		// A loaded repeat on the before side. Best-of-N is what keeps it
		// from making the after side look good.
		pairedRun(map[string]time.Duration{"point": 30 * us, "scan": 90 * us}),
	}}
	after := map[string][]measure.Result{"micro-read": {
		pairedRun(map[string]time.Duration{"point": 12 * us, "scan": 20 * us}),
		pairedRun(map[string]time.Duration{"point": 25 * us, "scan": 44 * us}),
	}}

	p := PairedAB(before, after, "", 1.10)
	if p.Metric != DefaultPairedMetric {
		t.Fatalf("Metric = %q, want %q", p.Metric, DefaultPairedMetric)
	}
	if len(p.Pairs) != 2 {
		t.Fatalf("Pairs = %v, want one per query", p.Pairs)
	}
	// Slowest first, so the row that needs looking at is on top.
	top := p.Pairs[0]
	if top.Query != "point" {
		t.Errorf("worst row is %q, want point", top.Query)
	}
	if top.Before != 10*us || top.After != 12*us {
		t.Errorf("point = %v to %v, want the best of each side, 10µs to 12µs", top.Before, top.After)
	}
	if got := top.Factor; got < 1.19 || got > 1.21 {
		t.Errorf("point factor = %.2f, want 12/10 = 1.20", got)
	}
	if top.BeforeRuns != 2 || top.AfterRuns != 2 {
		t.Errorf("point runs = %d and %d, want 2 and 2", top.BeforeRuns, top.AfterRuns)
	}
	if got := p.Pairs[1].Factor; got < 0.49 || got > 0.51 {
		t.Errorf("scan factor = %.2f, want 20/40 = 0.50", got)
	}
	if p.Pass() {
		t.Errorf("Pass() with a 1.20x row and a 1.10x threshold, want a failure")
	}
	if reg := p.Regressed(); len(reg) != 1 || reg[0].Query != "point" {
		t.Errorf("Regressed() = %v, want point alone", reg)
	}
}

func TestPairedABComparesTheMetricItIsAsked(t *testing.T) {
	us := time.Microsecond
	before := map[string][]measure.Result{"micro-read": {measure.Result{ByQuery: map[string]measure.Stat{
		"q": {Class: engine.PointRead, Count: 100, Min: 10 * us, P50: 20 * us, P99: 100 * us},
	}}}}
	after := map[string][]measure.Result{"micro-read": {measure.Result{ByQuery: map[string]measure.Stat{
		// Same floor, same median, a heavier tail. Only a p99 comparison
		// should see it.
		"q": {Class: engine.PointRead, Count: 100, Min: 10 * us, P50: 20 * us, P99: 300 * us},
	}}}}

	for _, tc := range []struct {
		metric string
		want   float64
	}{{"min", 1.0}, {"p50", 1.0}, {"p99", 3.0}} {
		p := PairedAB(before, after, tc.metric, 1.10)
		if len(p.Pairs) != 1 {
			t.Fatalf("%s: Pairs = %v, want one", tc.metric, p.Pairs)
		}
		if got := p.Pairs[0].Factor; got < tc.want-0.01 || got > tc.want+0.01 {
			t.Errorf("%s: factor = %.2f, want %.2f", tc.metric, got, tc.want)
		}
	}
}

// A workload only one side ran is not a regression and not a pass, it is
// missing, and reporting it either way would be a made up number.
func TestPairedABSkipsWhatOnlyOneSideRan(t *testing.T) {
	us := time.Microsecond
	before := map[string][]measure.Result{
		"micro-read": {pairedRun(map[string]time.Duration{"point": 10 * us, "gone": 10 * us})},
		"lonely":     {pairedRun(map[string]time.Duration{"point": 10 * us})},
	}
	after := map[string][]measure.Result{
		"micro-read": {pairedRun(map[string]time.Duration{"point": 10 * us})},
	}
	p := PairedAB(before, after, "min", 1.10)
	if len(p.Pairs) != 1 || p.Pairs[0].Workload != "micro-read" || p.Pairs[0].Query != "point" {
		t.Fatalf("Pairs = %v, want the one query both sides ran", p.Pairs)
	}
	if !p.Pass() {
		t.Errorf("Pass() = false on an unchanged query, want true")
	}
}

func TestPairedABSaysWhenNothingLinedUp(t *testing.T) {
	p := PairedAB(map[string][]measure.Result{}, map[string][]measure.Result{}, "min", 1.10)
	if !p.Pass() {
		t.Errorf("Pass() = false with no rows, want true: an empty comparison found no regression")
	}
	if got := p.Summary(); got == "" || len(p.Pairs) != 0 {
		t.Errorf("Summary() = %q with %d rows, want the no rows explanation", got, len(p.Pairs))
	}
}

// Two workloads that name a query the same are two rows, not one.
func TestPairedABKeepsWorkloadsApart(t *testing.T) {
	us := time.Microsecond
	before := map[string][]measure.Result{
		"micro-read":    {pairedRun(map[string]time.Duration{"micro-point": 10 * us})},
		"micro-uniform": {pairedRun(map[string]time.Duration{"micro-point": 10 * us})},
	}
	after := map[string][]measure.Result{
		"micro-read":    {pairedRun(map[string]time.Duration{"micro-point": 10 * us})},
		"micro-uniform": {pairedRun(map[string]time.Duration{"micro-point": 15 * us})},
	}
	p := PairedAB(before, after, "min", 1.10)
	if len(p.Pairs) != 2 {
		t.Fatalf("Pairs = %v, want one per workload", p.Pairs)
	}
	if p.Pairs[0].Workload != "micro-uniform" {
		t.Errorf("worst row is %s, want micro-uniform", p.Pairs[0].Workload)
	}
	if p.Median < 1.49 || p.Median > 1.51 {
		t.Errorf("Median = %.2f, want the upper of two rows, 15/10 = 1.50", p.Median)
	}
}

func TestPairedCostsComparesWhatARunSpent(t *testing.T) {
	before := map[string][]measure.Resource{"linkbench": {
		{CPUUserNs: 3_000_000_000, CPUSysNs: 400_000_000, ChildCPUUserNs: -1, ChildCPUSysNs: -1,
			MaxRSSBytes: 100 << 20, ChildMaxRSSBytes: -1, StoreBytes: 6 << 20, StoreGrowthBytes: 1 << 20},
		// A loaded repeat. Best of N is what keeps it out of the answer.
		{CPUUserNs: 9_000_000_000, CPUSysNs: 900_000_000, ChildCPUUserNs: -1, ChildCPUSysNs: -1,
			MaxRSSBytes: 140 << 20, ChildMaxRSSBytes: -1, StoreBytes: 6 << 20, StoreGrowthBytes: 1 << 20},
	}}
	after := map[string][]measure.Resource{"linkbench": {
		{CPUUserNs: 3_600_000_000, CPUSysNs: 400_000_000, ChildCPUUserNs: -1, ChildCPUSysNs: -1,
			MaxRSSBytes: 100 << 20, ChildMaxRSSBytes: -1, StoreBytes: 6 << 20, StoreGrowthBytes: 1 << 20},
	}}

	costs := PairedCosts(before, after)
	if len(costs) != len(PairedCostMetrics) {
		t.Fatalf("costs = %v, want one row per metric", costs)
	}
	cpu := costs[0]
	if cpu.Metric != "cpu" {
		t.Fatalf("first row is %q, want cpu", cpu.Metric)
	}
	if cpu.Before != 3_400_000_000 {
		t.Errorf("cpu before = %d, want the quietest repeat, user plus system", cpu.Before)
	}
	if got := cpu.Factor; got < 1.17 || got > 1.19 {
		t.Errorf("cpu factor = %.2f, want 4.0/3.4 = 1.18", got)
	}
	if cpu.BeforeRuns != 2 || cpu.AfterRuns != 1 {
		t.Errorf("cpu runs = %d and %d, want 2 and 1", cpu.BeforeRuns, cpu.AfterRuns)
	}
	if rss := costs[1]; rss.Metric != "peak rss" || rss.Before != 100<<20 || rss.Factor != 1 {
		t.Errorf("rss row = %+v, want the quietest peak on both sides", rss)
	}
}

// A metric the platform could not report is left out, not counted as nought,
// because a zero here reads as a run that cost nothing.
func TestPairedCostsSkipsWhatThePlatformCouldNotAnswer(t *testing.T) {
	unavailable := measure.Resource{
		CPUUserNs: -1, CPUSysNs: -1, ChildCPUUserNs: -1, ChildCPUSysNs: -1,
		MaxRSSBytes: -1, ChildMaxRSSBytes: -1, StoreBytes: 6 << 20, StoreGrowthBytes: -1,
	}
	costs := PairedCosts(
		map[string][]measure.Resource{"micro-read": {unavailable}},
		map[string][]measure.Resource{"micro-read": {unavailable}},
	)
	if len(costs) != 1 || costs[0].Metric != "store bytes" {
		t.Fatalf("costs = %v, want the one metric the platform answered", costs)
	}
}

func TestPairedCostsSkipsWorkloadsOnlyOneSideRan(t *testing.T) {
	r := measure.Resource{CPUUserNs: 1, CPUSysNs: 0, ChildCPUUserNs: -1, ChildCPUSysNs: -1,
		MaxRSSBytes: -1, ChildMaxRSSBytes: -1, StoreBytes: -1, StoreGrowthBytes: -1}
	costs := PairedCosts(
		map[string][]measure.Resource{"a": {r}, "b": {r}},
		map[string][]measure.Resource{"a": {r}},
	)
	if len(costs) != 1 || costs[0].Workload != "a" {
		t.Fatalf("costs = %v, want the workload both sides ran", costs)
	}
}
