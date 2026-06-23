// Package measure holds the open-model load generator, latency capture with
// coordinated-omission correction, nearest-rank percentiles, and the result
// schema. The types in this file are the five-metric output contract: every
// measured run produces a Result that carries the per-class latency distribution,
// throughput, errors, cold stats, load stats, and the full condition stamp (F9).
//
// See notes/Spec/2060/bench/06-metrics-and-measurement.md for the full design.
package measure

import (
	"sort"
	"time"

	"github.com/tamnd/graph-bench/target"
)

// Sample is one scheduled query execution, successful or not. The Latency is
// measured from the intended arrival time (coordinated-omission correction);
// a non-nil Err means the query failed and the sample is counted in Errors
// but excluded from the latency percentiles.
//
// QueryID is the workload query id (e.g. "snb-is2", "lsqb-q5"). It is empty
// for ad-hoc op slices built without a workload query. The per-query report
// uses it to aggregate ByQuery; the gate and per-class report use only Class.
type Sample struct {
	Class   target.Class
	QueryID string
	Latency time.Duration
	Rows    int
	Err     error
}

// Stat is the per-class latency distribution over the steady-state window.
// Errors are counted toward Count but excluded from the percentile slice,
// so P99 describes completed queries only. Throughput is queries/second over
// the measured window; zero for a single-client latency-only run or when
// the window duration was not supplied.
type Stat struct {
	Class      target.Class
	Count      int
	Errors     int
	P50        time.Duration
	P90        time.Duration
	P95        time.Duration
	P99        time.Duration
	Max        time.Duration
	Mean       time.Duration
	Throughput float64
}

// Result is the complete outcome of one measured run: per-class statistics,
// per-query statistics, cold-cache first-access statistics (F5), load stats,
// the latency-under-load curve, and the full condition stamp. Warmup samples
// are already excluded from Stats; every figure is from the steady-state window
// only. Result serializes to JSON for the lineage (doc 08) and is the input
// the gate (doc 07) checks.
type Result struct {
	Stats     map[target.Class]Stat // per-class latency distribution + throughput
	ByQuery   map[string]Stat       // per-query-id latency; populated when Sample.QueryID is set
	Cold      map[target.Class]Stat // cold-cache first-access (F5); empty for warm-only runs
	Load      target.LoadStats      // load time and on-disk size (section 1)
	Sweep     []SweepPoint          // latency-under-load curve (section 6.4)
	Condition Condition             // the full stamp (F9)
}

// SweepPoint is one concurrency point of the latency-under-load curve: the
// concurrency, the achieved throughput, and the p99 at that point, per class.
type SweepPoint struct {
	Concurrency int
	Class       target.Class
	Throughput  float64
	P99         time.Duration
}

// Condition is the stamp every published number carries (F9). It is captured at
// measurement time and is immutable after that. A Result whose Condition has
// any required field empty is marked incomplete and is not eligible for the lineage.
type Condition struct {
	// Engine
	Engine        string            // Target.Name(): "gr", "gr-bolt", "neo4j", "memgraph"
	EngineVersion string            // Target.Version(): queried live from the engine
	Plane         string            // "inproc", "bolt", "native"
	Config        map[string]string // declared, published per-engine config (F8)
	Tuned         bool              // tuned run shown beside out-of-the-box, never instead (F8)

	// Harness
	HarnessVersion string // graph-bench version
	HarnessCommit  string // git commit of the harness build

	// Data
	Dataset         string // dataset name, e.g. "snb" or "rmat"
	Scale           string // scale factor, e.g. "SF1", "scale-20"
	DatasetChecksum string // content checksum of the materialized dataset (F2)

	// Workload
	Workload string            // workload name, e.g. "snb-short", "lsqb", "micro-khop"
	Params   map[string]string // workload parameters that shaped the run

	// Cache and load model
	Cache       string  // "cold" or "warm" (F5)
	OfferedRate float64 // open-model offered queries/second
	Concurrency []int   // the concurrency points swept

	// Hardware and platform
	Hardware  string // CPU model, core count, RAM, storage class (F3)
	OS        string // OS and version
	GoVersion string // Go toolchain version

	// Statistics
	Repetitions int       // measured repetition count (section 7.1)
	Seed        int64     // fixed seed for parameter selection (section 7.3)
	Warmup      string    // "dynamic" or "fixed-20pct" (section 4.2)
	Timestamp   time.Time // when the run was measured
}

// percentile returns the nearest-rank percentile of an already-sorted slice of
// durations. p is in [0, 1]. An empty slice returns 0. The formula rounds up
// so p=1.0 returns the maximum: rank = ceil(n*p), clamped to [1, n].
func percentile(sorted []time.Duration, p float64) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	rank := int(float64(n)*p + 0.999999)
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}

// summarizeGroup turns a raw sample slice into a Stat. A non-nil Err counts
// toward Count and Errors but is excluded from the percentile slice, so the
// percentiles describe completed queries only. Throughput is successful queries
// per second over window; zero when window is zero.
func summarizeGroup(class target.Class, group []Sample, window time.Duration) Stat {
	stat := Stat{Class: class, Count: len(group)}
	lat := make([]time.Duration, 0, len(group))
	var sum time.Duration
	for _, s := range group {
		if s.Err != nil {
			stat.Errors++
			continue
		}
		lat = append(lat, s.Latency)
		sum += s.Latency
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	stat.P50 = percentile(lat, 0.50)
	stat.P90 = percentile(lat, 0.90)
	stat.P95 = percentile(lat, 0.95)
	stat.P99 = percentile(lat, 0.99)
	if n := len(lat); n > 0 {
		stat.Max = lat[n-1]
		stat.Mean = sum / time.Duration(n)
		if window > 0 {
			stat.Throughput = float64(n) / window.Seconds()
		}
	}
	return stat
}

// summarize turns raw steady-state samples into per-class and per-query-id
// statistics. A sample with a non-nil Err counts toward Count and Errors but
// is excluded from the latency slice. Throughput is successful queries per
// second over window; zero when window is zero.
func summarize(samples []Sample, window time.Duration) (byClass map[target.Class]Stat, byQuery map[string]Stat) {
	classBuckets := map[target.Class][]Sample{}
	queryBuckets := map[string][]Sample{}
	for _, s := range samples {
		classBuckets[s.Class] = append(classBuckets[s.Class], s)
		if s.QueryID != "" {
			queryBuckets[s.QueryID] = append(queryBuckets[s.QueryID], s)
		}
	}
	byClass = make(map[target.Class]Stat, len(classBuckets))
	for class, group := range classBuckets {
		byClass[class] = summarizeGroup(class, group, window)
	}
	if len(queryBuckets) > 0 {
		byQuery = make(map[string]Stat, len(queryBuckets))
		for qid, group := range queryBuckets {
			// Use the class of the first sample for the query-level Stat.
			byQuery[qid] = summarizeGroup(group[0].Class, group, window)
		}
	}
	return byClass, byQuery
}
