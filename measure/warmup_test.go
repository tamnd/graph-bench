package measure

import (
	"testing"
	"time"
)

// TestWarmedUpBasic checks the streak-and-minBuckets gate.
func TestWarmedUpBasic(t *testing.T) {
	// Five stable buckets at 10ms: change = 0 < 5%. streak=3, minBuckets=5.
	buckets := []time.Duration{
		10 * time.Millisecond,
		10 * time.Millisecond,
		10 * time.Millisecond,
		10 * time.Millisecond,
		10 * time.Millisecond,
	}
	if !warmedUp(buckets, 0.05, 3, 5) {
		t.Error("five identical buckets should be declared warm (streak=3, min=5)")
	}
}

// TestWarmedUpNotEnoughBuckets proves that fewer than minBuckets always returns false.
func TestWarmedUpNotEnoughBuckets(t *testing.T) {
	buckets := []time.Duration{10 * time.Millisecond, 10 * time.Millisecond}
	if warmedUp(buckets, 0.05, 1, 5) {
		t.Error("2 buckets with minBuckets=5 should not be declared warm")
	}
}

// TestWarmedUpStreakBroken proves that one high-change bucket resets stability.
func TestWarmedUpStreakBroken(t *testing.T) {
	// Four stable then one 50% jump: streak broken at the end.
	buckets := []time.Duration{
		10 * time.Millisecond,
		10 * time.Millisecond,
		10 * time.Millisecond,
		10 * time.Millisecond,
		15 * time.Millisecond, // 50% jump
	}
	if warmedUp(buckets, 0.05, 3, 5) {
		t.Error("50%% jump in last bucket should break the streak")
	}
}

// TestWarmedUpAfterRecovery proves that stability requires a full new streak
// after a disruption.
func TestWarmedUpAfterRecovery(t *testing.T) {
	// Spike at index 3, then three stable buckets.
	buckets := []time.Duration{
		10 * time.Millisecond, // stable
		10 * time.Millisecond, // stable
		10 * time.Millisecond, // stable
		50 * time.Millisecond, // spike
		10 * time.Millisecond, // recovery (large change from 50ms)
		10 * time.Millisecond, // stable
		10 * time.Millisecond, // stable
	}
	// The last three: prev[5]=10ms cur[6]=10ms ok; prev[4]=10ms cur[5]=10ms ok;
	// prev[3]=50ms cur[4]=10ms: change=0.8 > 0.05 -> not stable.
	if warmedUp(buckets, 0.05, 3, 5) {
		t.Error("streak check should look back 3 from end and see the spike in prev")
	}
}

// TestWarmedUpZeroPrev proves a zero prev bucket prevents declaration (avoids
// division by zero and a premature warm call at the very start).
func TestWarmedUpZeroPrev(t *testing.T) {
	buckets := []time.Duration{0, 0, 0, 0, 0}
	if warmedUp(buckets, 0.05, 3, 5) {
		t.Error("zero prev buckets should not declare warm")
	}
}

// TestWarmupConfigDefaultsFraction checks WarmupOps uses the 20% default.
func TestWarmupConfigDefaultsFraction(t *testing.T) {
	cfg := WarmupConfig{}
	if n := cfg.WarmupOps(100); n != 20 {
		t.Errorf("WarmupOps(100) = %d, want 20 (default 20%%)", n)
	}
}

// TestWarmupConfigExplicitFraction checks a custom fraction.
func TestWarmupConfigExplicitFraction(t *testing.T) {
	cfg := WarmupConfig{Fraction: 0.10}
	if n := cfg.WarmupOps(100); n != 10 {
		t.Errorf("WarmupOps(100) with 10%% = %d, want 10", n)
	}
}

// TestWarmupConfigCeiling proves WarmupOps is capped at total.
func TestWarmupConfigCeiling(t *testing.T) {
	cfg := WarmupConfig{Fraction: 2.0}
	if n := cfg.WarmupOps(50); n != 50 {
		t.Errorf("WarmupOps capped at total: got %d, want 50", n)
	}
}

// TestWarmupDetectorStabilizes feeds a WarmupDetector a series of stable
// latencies and confirms Stable() becomes true after enough buckets.
func TestWarmupDetectorStabilizes(t *testing.T) {
	cfg := WarmupConfig{
		DynamicWarmup: true,
		BucketWidth:   10 * time.Millisecond,
		Tol:           0.05,
		Streak:        3,
		MinBuckets:    4,
		MaxWarmup:     time.Second,
		Fraction:      0.20,
	}
	d := NewWarmupDetector(cfg)

	base := time.Now()
	// Feed 5 buckets of 10 samples each, latency stable at 1ms.
	for b := 0; b < 5; b++ {
		for s := 0; s < 10; s++ {
			t := base.Add(time.Duration(b)*10*time.Millisecond + time.Duration(s)*time.Millisecond)
			d.Add(1*time.Millisecond, t)
		}
	}
	// After 5 buckets (each 10ms wide), all p99 = 1ms -> change = 0 < 5%.
	// MinBuckets=4, streak=3: should be stable.
	if !d.Stable() {
		t.Errorf("detector should be stable after 5 uniform buckets; buckets=%v", d.Buckets())
	}
}

// TestWarmupDetectorNotStableOnSpike proves a spike during warmup prevents early declaration.
func TestWarmupDetectorNotStableOnSpike(t *testing.T) {
	cfg := WarmupConfig{
		BucketWidth: 10 * time.Millisecond,
		Tol:         0.05,
		Streak:      3,
		MinBuckets:  4,
	}
	d := NewWarmupDetector(cfg)

	base := time.Now()
	// 3 stable buckets at 1ms, then 1 spike bucket at 100ms.
	for b := 0; b < 3; b++ {
		for s := 0; s < 5; s++ {
			ts := base.Add(time.Duration(b)*10*time.Millisecond + time.Duration(s)*time.Millisecond)
			d.Add(1*time.Millisecond, ts)
		}
	}
	// Spike bucket
	for s := 0; s < 5; s++ {
		ts := base.Add(30*time.Millisecond + time.Duration(s)*time.Millisecond)
		d.Add(100*time.Millisecond, ts)
	}
	if d.Stable() {
		t.Error("detector declared stable despite spike bucket")
	}
}

// TestSortedCopy proves sortedCopy sorts a copy without modifying the original.
func TestSortedCopy(t *testing.T) {
	orig := []time.Duration{5, 2, 8, 1, 3}
	cp := sortedCopy(orig)

	// Original must be unchanged.
	if orig[0] != 5 {
		t.Error("sortedCopy modified the original")
	}
	// Copy must be sorted.
	for i := 1; i < len(cp); i++ {
		if cp[i] < cp[i-1] {
			t.Errorf("sorted copy not sorted at index %d: %v", i, cp)
		}
	}
}
