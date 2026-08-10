package measure

import (
	"math"
	"time"
)

// WarmupConfig controls the stabilization criterion for warmup (spec 08 §3).
// The harness collects per-bucket p99s during warmup and declares the engine
// warm when three consecutive windows agree within 15%. A fixed floor
// (default 3 s or 200 ops, whichever is later) prevents premature declaration
// on a lucky flat patch; a hard ceiling (default 60 s) prevents the engine
// from warming forever — hitting it flags "capped" in the stamp instead of
// failing.
//
// The fixed-fraction path (WarmupOps) discards the first Fraction of
// scheduled ops and is cheaper to compute; CI uses it because
// reproducibility matters more than confirming convergence on a short run.
type WarmupConfig struct {
	// BucketWidth is the bucket size for the stability detector (default 1s).
	BucketWidth time.Duration

	// Tol is the relative change tolerance between consecutive bucket p99s;
	// default 0.15 (15%, spec 08 §3).
	Tol float64

	// Streak is the number of consecutive in-tolerance buckets required to
	// declare the engine warm; default 3.
	Streak int

	// MinBuckets is the minimum number of completed buckets before
	// stabilization can be declared; default 5.
	MinBuckets int

	// MinDuration is the fixed time floor: stability is never declared before
	// this much warmup has elapsed. Default 3s (spec 08 §3).
	MinDuration time.Duration

	// MinOps is the fixed op floor: stability is never declared before this
	// many warmup samples have been observed. Default 200 (spec 08 §3).
	MinOps int

	// MaxWarmup caps the warmup duration; default 60s. Hitting the cap is
	// reported by Outcome() as "capped", stamped, not failed.
	MaxWarmup time.Duration

	// Fraction is used by the fixed path: that fraction of the scheduled ops
	// are fired-but-not-recorded. Default 0.20.
	Fraction float64
}

// defaults fills zero-valued fields with the spec defaults.
func (w WarmupConfig) defaults() WarmupConfig {
	if w.BucketWidth == 0 {
		w.BucketWidth = time.Second
	}
	if w.Tol == 0 {
		w.Tol = 0.15
	}
	if w.Streak == 0 {
		w.Streak = 3
	}
	if w.MinBuckets == 0 {
		w.MinBuckets = 5
	}
	if w.MinDuration == 0 {
		w.MinDuration = 3 * time.Second
	}
	if w.MinOps == 0 {
		w.MinOps = 200
	}
	if w.MaxWarmup == 0 {
		w.MaxWarmup = 60 * time.Second
	}
	if w.Fraction == 0 {
		w.Fraction = 0.20
	}
	return w
}

// WarmupOps returns the number of ops to fire-but-not-record based on the
// fixed-fraction rule: ceil(n * Fraction). It is used both by the fixed path
// and as a floor for the detector path.
func (w WarmupConfig) WarmupOps(total int) int {
	cfg := w.defaults()
	n := int(math.Ceil(float64(total) * cfg.Fraction))
	if n > total {
		n = total
	}
	return n
}

// warmedUp reports whether the engine has stabilized under the moving-window
// criterion: the relative change in bucket p99 between successive windows has
// stayed below tol for streak consecutive buckets, and at least minBuckets
// have elapsed so a single lucky flat patch does not end warmup prematurely.
// The buckets slice holds the per-bucket p99 in arrival order.
//
// The state machine resets if a later bucket breaks the streak, so an engine
// that appears to settle and then hits a compaction pause does not get
// declared warm during the lull; it must settle again afterward.
func warmedUp(buckets []time.Duration, tol float64, streak, minBuckets int) bool {
	if len(buckets) < minBuckets || len(buckets) <= streak {
		return false
	}
	for i := len(buckets) - streak; i < len(buckets); i++ {
		prev, cur := buckets[i-1], buckets[i]
		if prev == 0 {
			return false
		}
		change := math.Abs(float64(cur-prev)) / float64(prev)
		if change > tol {
			return false
		}
	}
	return true
}

// WarmupDetector collects per-bucket p99s during a warmup window and reports
// when the engine has stabilized (spec 08 §3). Feed samples with Add as they
// arrive; call Stable to check whether stabilization has been declared and
// Outcome for the stamp field. The runner calls the detector on every warm
// measurement — in v1 it was dead code, and silent non-wiring is not allowed
// (spec 02 §5.4).
type WarmupDetector struct {
	cfg     WarmupConfig
	bucket  []time.Duration // latencies in the current bucket
	buckets []time.Duration // completed bucket p99s
	tick    time.Time       // start of the current bucket
	first   time.Time       // first sample's time, for the MinDuration floor
	ops     int             // samples observed, for the MinOps floor
	stable  bool
}

// NewWarmupDetector returns a WarmupDetector configured by cfg.
func NewWarmupDetector(cfg WarmupConfig) *WarmupDetector {
	return &WarmupDetector{cfg: cfg.defaults()}
}

// Add records a latency sample. now is the sample's intended arrival time.
func (d *WarmupDetector) Add(latency time.Duration, now time.Time) {
	if d.stable {
		return
	}
	d.ops++
	if d.first.IsZero() {
		d.first = now
	}
	if d.tick.IsZero() {
		d.tick = now
	}
	if now.Sub(d.tick) >= d.cfg.BucketWidth {
		// Flush the current bucket.
		d.buckets = append(d.buckets, percentile(sortedCopy(d.bucket), 0.99))
		d.bucket = d.bucket[:0]
		d.tick = now
		cfg := d.cfg
		// The fixed floor (spec 08 §3): 3s or 200 ops, whichever is later.
		floor := d.ops >= cfg.MinOps && now.Sub(d.first) >= cfg.MinDuration
		if floor && warmedUp(d.buckets, cfg.Tol, cfg.Streak, cfg.MinBuckets) {
			d.stable = true
		}
	}
	d.bucket = append(d.bucket, latency)
}

// Stable reports whether the engine has been declared warm.
func (d *WarmupDetector) Stable() bool { return d.stable }

// Outcome returns the stamp value for Condition.WarmupOutcome: "stable" when
// the detector declared stability, "capped" otherwise — the caller invokes it
// when warmup ends, either by stabilization or by hitting MaxWarmup
// (spec 08 §3: hitting the cap flags warmup:capped instead of failing).
func (d *WarmupDetector) Outcome() string {
	if d.stable {
		return "stable"
	}
	return "capped"
}

// Buckets returns the per-bucket p99 series for diagnostic reporting.
func (d *WarmupDetector) Buckets() []time.Duration { return d.buckets }

// sortedCopy returns a sorted copy of ds without modifying the original.
func sortedCopy(ds []time.Duration) []time.Duration {
	cp := make([]time.Duration, len(ds))
	copy(cp, ds)
	// insertion sort: buckets are small so this is fine
	for i := 1; i < len(cp); i++ {
		for j := i; j > 0 && cp[j] < cp[j-1]; j-- {
			cp[j], cp[j-1] = cp[j-1], cp[j]
		}
	}
	return cp
}
