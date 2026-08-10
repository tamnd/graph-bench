// Package measure holds the open-model load generator, latency capture with
// coordinated-omission correction, nearest-rank percentiles, the analytics
// repetition protocol, and the result schema. The types in this file are the
// six-metric output contract (spec 08 §1): every measured run produces a
// Result carrying the per-class latency distribution, throughput, errors,
// cold stats, load stats, and the full Condition stamp (08 §7).
//
// See notes/Spec/2064g/bench/08-measurement-and-gates.md for the full design.
package measure

import (
	"math"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// LatencyModel names which clock a run's latencies were measured against
// (spec 08 §2). A report or gate uses it to refuse comparing a service-time
// number to an open-model number, which are not the same quantity and must
// never be divided.
type LatencyModel string

const (
	// ServiceTimeLatency is measured from actual dispatch, the moment the worker
	// pool admits the op, to completion: the engine's per-query service time with
	// no queueing in it. Count-mode runs (no offered rate) use it, because with no
	// rate there is no arrival schedule to be late against, only a serialized
	// queue whose depth would otherwise masquerade as latency.
	ServiceTimeLatency LatencyModel = "service-time"
	// OpenModelLatency is measured from the op's intended arrival, so time spent
	// waiting for the engine to catch up under an offered rate lands in the number
	// (coordinated-omission correction). Rate-limited runs use it.
	OpenModelLatency LatencyModel = "open-model"
)

// Sample is one scheduled query execution, successful or not. Latency is
// measured against the run's LatencyModel: from intended arrival for an
// open-model (rate-limited) run, from actual dispatch for a count-mode run.
// A non-nil Err means the query failed and the sample is counted in Errors
// but excluded from the latency percentiles (spec 08 §1, F10).
//
// QueryID is the workload query id (e.g. "is3", "lsqb-q5"). It is empty for
// ad-hoc op slices built without a workload query. The per-query report uses
// it to aggregate ByQuery; the gate and per-class report use only Class.
type Sample struct {
	Class   engine.Class
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
	Class         engine.Class
	Count         int
	Errors        int
	Min           time.Duration
	P50           time.Duration
	P90           time.Duration
	P95           time.Duration
	P99           time.Duration
	Max           time.Duration
	Mean          time.Duration
	StdDev        time.Duration // population standard deviation of the latencies
	Throughput    float64       // completed queries per second over the window
	RowThroughput float64       // result rows per second over the window
}

// Result is the complete outcome of one measured run: per-class statistics,
// per-query statistics, cold-cache first-access statistics (F4: cold and warm
// stay separate sections), load stats, the latency-under-load curve, and the
// full Condition stamp. Warmup samples are already excluded from Stats; every
// figure is from the steady-state window only.
type Result struct {
	Stats     map[engine.Class]Stat // per-class latency distribution + throughput
	ByQuery   map[string]Stat       // per-query-id latency; populated when Sample.QueryID is set
	Cold      map[engine.Class]Stat // cold-cache first-access; empty for warm-only runs
	Load      engine.LoadStats      // load time and on-disk size (spec 08 §1 metric 3/4)
	Resource  Resource              // memory and disk cost of the run (resource.go)
	Sweep     []SweepPoint          // latency-under-load curve (sweep.go)
	Latency   LatencyModel          // which clock the latencies were measured against
	Condition Condition             // the full stamp (spec 08 §7)
}

// SweepPoint is one concurrency point of the latency-under-load curve: the
// concurrency, the achieved throughput, and the p99 at that point, per class.
type SweepPoint struct {
	Concurrency int
	Class       engine.Class
	Throughput  float64
	P99         time.Duration
}

// Hardware describes the machine a run was measured on. It is collected by
// CollectHardware, never hand-entered (spec 08 §7).
type Hardware struct {
	CPU      string // CPU model string, "unknown" when unreadable
	Cores    int    // logical core count
	RAMBytes int64  // physical memory, -1 when unreadable
	OS       string // runtime.GOOS
	Arch     string // runtime.GOARCH
}

// Condition is the stamp every published number carries (spec 08 §7). It is
// captured at measurement time and immutable after that. The runner's
// buildCondition has a test asserting no field is zero-valued after a real
// run — v1 shipped empty Hardware/Seed fields for a year.
type Condition struct {
	// Harness.
	HarnessVersion string // graph-bench version
	HarnessCommit  string // git commit of the harness build

	// Engine.
	Engine        string            // engine.Info().Name: "zu", "neo4j", "ladybug", "memgraph"
	EngineVersion string            // Session.Version(): queried live from the engine
	Plane         string            // "inproc", "bolt", "subprocess", "native"
	DialectUsed   map[string]string // query id -> dialect its text was resolved to
	Config        map[string]string // declared, published per-engine config (F8)
	Tuned         bool              // tuned run shown beside out-of-the-box, never instead (F8)

	// Data.
	Dataset         string // dataset name, e.g. "snb-sf1", "grid"
	Scale           string // scale factor, e.g. "SF1", "scale-20"
	DatasetChecksum string // content checksum of the materialized dataset (F2)
	ParamsChecksum  string // checksum of the parameter pools the run drew from

	// Workload.
	Workload string // workload name, e.g. "snb-short", "lsqb", "micro-read"
	MixSeed  int64  // seed of the mixed-schedule PRNG (mixed.go)

	// Measurement model.
	LatencyModel   LatencyModel // "service-time" or "open-model" (spec 08 §2)
	Rate           float64      // open-model offered queries/second, 0 in count mode
	Concurrency    []int        // the concurrency points run or swept
	WarmupOutcome  string       // "stable" or "capped" (spec 08 §3)
	Cache          string       // "cold", "cool", or "warm" (spec 08 §4)
	ColdProtocol   string       // page-cache drop protocol used: "purge", "drop_caches", "none"
	ValidationMode string       // "full" or "sampled" (spec 08 §5)
	Repetitions    int          // analytics repetition count (spec 08 §4)

	// Platform and time.
	Hardware   Hardware  // collected, not hand-entered (spec 08 §7)
	StartedAt  time.Time // when the measured window opened
	FinishedAt time.Time // when the measured window closed
}

// CollectHardware populates a Hardware stamp from the runtime and, best
// effort, the platform (sysctl on darwin, /proc on linux). It never returns
// empty strings: an unreadable CPU model reads "unknown", unreadable RAM
// reads -1, so an incomplete stamp is visible instead of silently blank.
func CollectHardware() Hardware {
	h := Hardware{
		CPU:      cpuModel(),
		Cores:    runtime.NumCPU(),
		RAMBytes: ramBytes(),
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
	}
	if h.CPU == "" {
		h.CPU = "unknown"
	}
	return h
}

// cpuModel reads the CPU model string from the platform, or "unknown".
func cpuModel() string {
	switch runtime.GOOS {
	case "darwin":
		if out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
			if s := strings.TrimSpace(string(out)); s != "" {
				return s
			}
		}
	case "linux":
		if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "model name" {
					if s := strings.TrimSpace(v); s != "" {
						return s
					}
				}
			}
		}
	}
	return "unknown"
}

// ramBytes reads physical memory in bytes from the platform, or -1.
func ramBytes() int64 {
	switch runtime.GOOS {
	case "darwin":
		if out, err := exec.Command("sysctl", "-n", "hw.memsize").Output(); err == nil {
			if n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil && n > 0 {
				return n
			}
		}
	case "linux":
		if data, err := os.ReadFile("/proc/meminfo"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if v, ok := strings.CutPrefix(line, "MemTotal:"); ok {
					fields := strings.Fields(v)
					if len(fields) >= 1 {
						if kb, err := strconv.ParseInt(fields[0], 10, 64); err == nil && kb > 0 {
							return kb * 1024
						}
					}
				}
			}
		}
	}
	return -1
}

// percentile returns the nearest-rank percentile of an already-sorted slice of
// durations (spec 08 §1: rank = ceil(n*p), clamped to [1, n], computed on the
// raw sample array, never on histograms). p is in [0, 1]. An empty slice
// returns 0. The formula rounds up so p=1.0 returns the maximum.
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
// percentiles describe completed queries only (F10). Throughput is successful
// queries per second over window; zero when window is zero.
func summarizeGroup(class engine.Class, group []Sample, window time.Duration) Stat {
	stat := Stat{Class: class, Count: len(group)}
	lat := make([]time.Duration, 0, len(group))
	var sum time.Duration
	var rows int
	for _, s := range group {
		if s.Err != nil {
			stat.Errors++
			continue
		}
		lat = append(lat, s.Latency)
		sum += s.Latency
		rows += s.Rows
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	stat.P50 = percentile(lat, 0.50)
	stat.P90 = percentile(lat, 0.90)
	stat.P95 = percentile(lat, 0.95)
	stat.P99 = percentile(lat, 0.99)
	if n := len(lat); n > 0 {
		stat.Min = lat[0]
		stat.Max = lat[n-1]
		stat.Mean = sum / time.Duration(n)
		stat.StdDev = stddev(lat, stat.Mean)
		if window > 0 {
			stat.Throughput = float64(n) / window.Seconds()
			stat.RowThroughput = float64(rows) / window.Seconds()
		}
	}
	return stat
}

// stddev returns the population standard deviation of the latency slice about
// mean, as a duration. Fewer than two samples have zero deviation. It is the
// spread beside the percentiles: a low p50 with a high stddev is a bimodal
// engine, not a uniformly fast one.
func stddev(lat []time.Duration, mean time.Duration) time.Duration {
	if len(lat) < 2 {
		return 0
	}
	m := float64(mean)
	var sumSq float64
	for _, d := range lat {
		diff := float64(d) - m
		sumSq += diff * diff
	}
	return time.Duration(math.Sqrt(sumSq / float64(len(lat))))
}

// summarize turns raw steady-state samples into per-class and per-query-id
// statistics. A sample with a non-nil Err counts toward Count and Errors but
// is excluded from the latency slice. Throughput is successful queries per
// second over window; zero when window is zero.
func summarize(samples []Sample, window time.Duration) (byClass map[engine.Class]Stat, byQuery map[string]Stat) {
	classBuckets := map[engine.Class][]Sample{}
	queryBuckets := map[string][]Sample{}
	for _, s := range samples {
		classBuckets[s.Class] = append(classBuckets[s.Class], s)
		if s.QueryID != "" {
			queryBuckets[s.QueryID] = append(queryBuckets[s.QueryID], s)
		}
	}
	byClass = make(map[engine.Class]Stat, len(classBuckets))
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
