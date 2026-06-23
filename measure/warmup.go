package measure

import (
	"math"
	"time"
)

// WarmupConfig controls the stabilization criterion for dynamic warmup.
// The harness collects per-second bucket p99s during warmup and declares the
// engine warm when consecutive buckets stop trending. A hard floor prevents
// premature declaration on a lucky flat patch; a hard ceiling prevents the
// engine from warming forever.
//
// The fixed-fraction path (DynamicWarmup=false) discards the first Fraction of
// scheduled ops and is cheaper to compute. CI uses the fixed path because
// reproducibility matters more than confirming convergence on a short run.
type WarmupConfig struct {
	// DynamicWarmup enables the moving-window stabilization criterion. When
	// false, WarmupOps is used directly.
	DynamicWarmup bool

	// BucketWidth is the bucket size for dynamic warmup (default 1 second).
	BucketWidth time.Duration

	// Tol is the relative change tolerance between consecutive bucket p99s;
	// default 0.05 (5%).
	Tol float64

	// Streak is the number of consecutive in-tolerance buckets required to
	// declare the engine warm; default 3.
	Streak int

	// MinBuckets is the minimum number of buckets before stabilization can be
	// declared; default is derived from the hard-floor warmup.
	MinBuckets int

	// MaxWarmup caps the warmup duration; default 60s (30s in CI).
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
		w.Tol = 0.05
	}
	if w.Streak == 0 {
		w.Streak = 3
	}
	if w.MinBuckets == 0 {
		w.MinBuckets = 5
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
// and as the hard-floor for the dynamic path.
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
// that appears to settle and then hits a compaction pause does not get declared
// warm during the lull; it must settle again afterward.
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
// when the engine has stabilized. Feed samples with Add as they arrive; call
// Stable to check whether stabilization has been declared.
type WarmupDetector struct {
	cfg     WarmupConfig
	bucket  []time.Duration // latencies in the current bucket
	buckets []time.Duration // completed bucket p99s
	tick    time.Time       // start of the current bucket
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
	if d.tick.IsZero() {
		d.tick = now
	}
	if now.Sub(d.tick) >= d.cfg.BucketWidth {
		// Flush the current bucket.
		d.buckets = append(d.buckets, percentile(sortedCopy(d.bucket), 0.99))
		d.bucket = d.bucket[:0]
		d.tick = now
		cfg := d.cfg
		if warmedUp(d.buckets, cfg.Tol, cfg.Streak, cfg.MinBuckets) {
			d.stable = true
		}
	}
	d.bucket = append(d.bucket, latency)
}

// Stable reports whether the engine has been declared warm.
func (d *WarmupDetector) Stable() bool { return d.stable }

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
