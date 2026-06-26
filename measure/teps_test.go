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
