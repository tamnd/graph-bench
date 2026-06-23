package measure

import (
	"errors"
	"testing"
	"time"

	"github.com/tamnd/graph-bench/target"
)

// ms is a helper to make durations readable in test literals.
func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

// TestPercentileNearestRank checks the nearest-rank formula on a well-known
// sorted slice where the expected values follow from the closed form.
func TestPercentileNearestRank(t *testing.T) {
	// Ten samples, evenly spaced 1ms apart: [1,2,3,4,5,6,7,8,9,10]ms.
	sorted := make([]time.Duration, 10)
	for i := range sorted {
		sorted[i] = ms(i + 1)
	}

	cases := []struct {
		p    float64
		want time.Duration
	}{
		// p50: rank = ceil(10*0.5) = 5 -> sorted[4] = 5ms
		{0.50, ms(5)},
		// p90: rank = ceil(10*0.9) = 9 -> sorted[8] = 9ms
		{0.90, ms(9)},
		// p95: rank = ceil(10*0.95) = 10 -> sorted[9] = 10ms
		{0.95, ms(10)},
		// p99: rank = ceil(10*0.99) = 10 -> sorted[9] = 10ms
		{0.99, ms(10)},
		// p100 (max): rank clamped to 10 -> sorted[9] = 10ms
		{1.00, ms(10)},
	}
	for _, c := range cases {
		got := percentile(sorted, c.p)
		if got != c.want {
			t.Errorf("percentile(sorted, %.2f) = %v, want %v", c.p, got, c.want)
		}
	}
}

// TestPercentileEmpty proves an empty slice returns zero.
func TestPercentileEmpty(t *testing.T) {
	if got := percentile(nil, 0.99); got != 0 {
		t.Errorf("percentile(nil, 0.99) = %v, want 0", got)
	}
	if got := percentile([]time.Duration{}, 0.50); got != 0 {
		t.Errorf("percentile([], 0.50) = %v, want 0", got)
	}
}

// TestPercentileSingleElement proves a one-sample slice returns that sample for
// every percentile, including p0 (rank clamped to 1).
func TestPercentileSingleElement(t *testing.T) {
	sorted := []time.Duration{ms(42)}
	for _, p := range []float64{0, 0.50, 0.99, 1.0} {
		if got := percentile(sorted, p); got != ms(42) {
			t.Errorf("percentile([42ms], %.2f) = %v, want 42ms", p, got)
		}
	}
}

// TestSummarizeBasic checks per-class grouping, percentile values, and mean.
func TestSummarizeBasic(t *testing.T) {
	samples := []Sample{
		{Class: target.Traversal, Latency: ms(1)},
		{Class: target.Traversal, Latency: ms(3)},
		{Class: target.Traversal, Latency: ms(2)},
		{Class: target.PointRead, Latency: ms(10)},
	}
	window := 4 * time.Second

	stats, _ := summarize(samples, window)

	tr, ok := stats[target.Traversal]
	if !ok {
		t.Fatal("no Traversal stat")
	}
	if tr.Count != 3 {
		t.Errorf("Traversal.Count = %d, want 3", tr.Count)
	}
	if tr.Errors != 0 {
		t.Errorf("Traversal.Errors = %d, want 0", tr.Errors)
	}
	// sorted latencies: [1,2,3]ms. p50=ceil(3*0.5)=2 -> sorted[1]=2ms.
	if tr.P50 != ms(2) {
		t.Errorf("Traversal.P50 = %v, want 2ms", tr.P50)
	}
	// p99=ceil(3*0.99)=3 -> sorted[2]=3ms = Max.
	if tr.P99 != ms(3) {
		t.Errorf("Traversal.P99 = %v, want 3ms", tr.P99)
	}
	if tr.Max != ms(3) {
		t.Errorf("Traversal.Max = %v, want 3ms", tr.Max)
	}
	if tr.Mean != ms(2) {
		t.Errorf("Traversal.Mean = %v, want 2ms", tr.Mean)
	}
	// Throughput: 3 successful queries in 4s -> 0.75 q/s.
	if tr.Throughput != 0.75 {
		t.Errorf("Traversal.Throughput = %v, want 0.75", tr.Throughput)
	}

	pr, ok := stats[target.PointRead]
	if !ok {
		t.Fatal("no PointRead stat")
	}
	if pr.Count != 1 || pr.P99 != ms(10) {
		t.Errorf("PointRead stat unexpected: %+v", pr)
	}
}

// TestSummarizeErrorsExcludedFromLatency proves that error samples increment
// Errors and Count but are not included in the percentile slice.
func TestSummarizeErrorsExcludedFromLatency(t *testing.T) {
	errFoo := errors.New("timeout")
	samples := []Sample{
		{Class: target.Write, Latency: ms(5)},
		{Class: target.Write, Latency: ms(1000), Err: errFoo}, // error sample
		{Class: target.Write, Latency: ms(6)},
	}
	stats, _ := summarize(samples, time.Second)
	w, ok := stats[target.Write]
	if !ok {
		t.Fatal("no Write stat")
	}
	if w.Count != 3 {
		t.Errorf("Count = %d, want 3", w.Count)
	}
	if w.Errors != 1 {
		t.Errorf("Errors = %d, want 1", w.Errors)
	}
	// Percentile computed over [5,6]ms only; the 1000ms error is excluded.
	if w.Max > ms(10) {
		t.Errorf("Max = %v, error latency leaked into distribution", w.Max)
	}
	// p99 over [5,6]: ceil(2*0.99)=2 -> sorted[1] = 6ms.
	if w.P99 != ms(6) {
		t.Errorf("P99 = %v, want 6ms", w.P99)
	}
}

// TestSummarizeZeroWindow proves Throughput is zero when window is zero.
func TestSummarizeZeroWindow(t *testing.T) {
	samples := []Sample{
		{Class: target.Traversal, Latency: ms(1)},
		{Class: target.Traversal, Latency: ms(2)},
	}
	stats, _ := summarize(samples, 0)
	if stats[target.Traversal].Throughput != 0 {
		t.Errorf("Throughput = %v with zero window, want 0", stats[target.Traversal].Throughput)
	}
}

// TestSummarizeAllErrors proves a class where every sample is an error produces
// zero latency percentiles (no panic from an empty slice).
func TestSummarizeAllErrors(t *testing.T) {
	errFoo := errors.New("conn refused")
	samples := []Sample{
		{Class: target.Analytical, Err: errFoo},
		{Class: target.Analytical, Err: errFoo},
	}
	stats, _ := summarize(samples, time.Second)
	a, ok := stats[target.Analytical]
	if !ok {
		t.Fatal("no Analytical stat")
	}
	if a.Count != 2 || a.Errors != 2 {
		t.Errorf("Count=%d Errors=%d, want 2/2", a.Count, a.Errors)
	}
	// All percentiles must be zero (no successful samples to rank).
	for _, d := range []time.Duration{a.P50, a.P90, a.P95, a.P99, a.Max, a.Mean} {
		if d != 0 {
			t.Errorf("latency stat non-zero on all-error class: %v", d)
		}
	}
}

// TestResultZeroValue proves the zero value of Result is valid (no nil-map panics).
func TestResultZeroValue(t *testing.T) {
	var r Result
	if r.Stats != nil {
		t.Error("zero Result.Stats should be nil")
	}
	if r.Cold != nil {
		t.Error("zero Result.Cold should be nil")
	}
	if len(r.Sweep) != 0 {
		t.Error("zero Result.Sweep should be empty")
	}
}

// TestConditionFields proves the Condition fields are settable and their zero
// values are the expected empty values (no hidden defaults).
func TestConditionFields(t *testing.T) {
	c := Condition{
		Engine:        "gr",
		EngineVersion: "0.1.0",
		Plane:         "inproc",
		Tuned:         false,
		Cache:         "warm",
		Repetitions:   5,
		Seed:          42,
		Warmup:        "fixed-20pct",
	}
	if c.Engine != "gr" {
		t.Errorf("Engine = %q", c.Engine)
	}
	if c.Cache != "warm" {
		t.Errorf("Cache = %q", c.Cache)
	}
	if c.Seed != 42 {
		t.Errorf("Seed = %d", c.Seed)
	}
}
