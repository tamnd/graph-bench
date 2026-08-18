package measure

import (
	"math"
	"testing"
	"time"
)

// TestTEPS checks the rate is edges over seconds and that degenerate inputs return
// zero rather than an infinity or a negative.
func TestTEPS(t *testing.T) {
	if got := TEPS(1_000_000, time.Second); got != 1_000_000 {
		t.Errorf("TEPS(1e6, 1s) = %g, want 1e6", got)
	}
	if got := TEPS(500, 500*time.Millisecond); got != 1000 {
		t.Errorf("TEPS(500, 500ms) = %g, want 1000", got)
	}
	if got := TEPS(100, 0); got != 0 {
		t.Errorf("TEPS with zero duration = %g, want 0", got)
	}
	if got := TEPS(0, time.Second); got != 0 {
		t.Errorf("TEPS with zero edges = %g, want 0", got)
	}
	if got := TEPS(100, -time.Second); got != 0 {
		t.Errorf("TEPS with negative duration = %g, want 0", got)
	}
}

// TestHarmonicMeanTEPS checks the rate-correct aggregation: the harmonic mean of
// two rates is below their arithmetic mean and skips zero-rate runs.
func TestHarmonicMeanTEPS(t *testing.T) {
	got := HarmonicMeanTEPS([]float64{1000, 3000})
	want := 2.0 / (1.0/1000 + 1.0/3000) // 1500
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("HarmonicMeanTEPS([1000,3000]) = %g, want %g", got, want)
	}
	if got >= 2000 {
		t.Errorf("harmonic mean %g should be below the arithmetic mean 2000", got)
	}
	if got := HarmonicMeanTEPS([]float64{0, 0}); got != 0 {
		t.Errorf("HarmonicMeanTEPS(all zero) = %g, want 0", got)
	}
	if got := HarmonicMeanTEPS(nil); got != 0 {
		t.Errorf("HarmonicMeanTEPS(nil) = %g, want 0", got)
	}
}

// TestNewTraversal checks the section carries a rate per kept repetition and
// the harmonic mean of them, and that a kernel with nothing to traverse comes
// back zeroed instead of infinite.
func TestNewTraversal(t *testing.T) {
	got := NewTraversal("7", 2000, []time.Duration{time.Millisecond, 2 * time.Millisecond})
	if got.Source != "7" || got.Edges != 2000 {
		t.Errorf("source/edges = %q/%d, want 7/2000", got.Source, got.Edges)
	}
	want := []float64{2e6, 1e6}
	if len(got.PerRep) != len(want) {
		t.Fatalf("PerRep = %v, want %v", got.PerRep, want)
	}
	for i, w := range want {
		if got.PerRep[i] != w {
			t.Errorf("PerRep[%d] = %g, want %g", i, got.PerRep[i], w)
		}
	}
	// The harmonic mean of 2e6 and 1e6 is 2/(1/2e6 + 1/1e6).
	if wantHM := 2 / (1/2e6 + 1/1e6); got.HarmonicMean != wantHM {
		t.Errorf("HarmonicMean = %g, want %g", got.HarmonicMean, wantHM)
	}
	if empty := NewTraversal("7", 0, []time.Duration{time.Millisecond}); empty.HarmonicMean != 0 {
		t.Errorf("no edges reached gave rate %g, want 0", empty.HarmonicMean)
	}
}
