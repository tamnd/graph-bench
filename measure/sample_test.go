package measure

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// TestSamplerKeepsPeakAndLast proves the sampler reports the largest reading
// it saw and the last one, which are the two numbers a memory row needs: what
// the engine had to be given and what it settled back to.
func TestSamplerKeepsPeakAndLast(t *testing.T) {
	var n atomic.Int64
	readings := []int64{100, 900, 300}
	s := NewSampler(time.Millisecond, func() int64 {
		i := n.Add(1) - 1
		if i >= int64(len(readings)) {
			return readings[len(readings)-1]
		}
		return readings[i]
	})
	// Wait for the whole sequence rather than a fixed sleep, so a slow box
	// does not turn a missed tick into a failure.
	deadline := time.Now().Add(5 * time.Second)
	for n.Load() < int64(len(readings)) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	got := s.Stop()
	if got.PeakBytes != 900 {
		t.Errorf("PeakBytes = %d, want 900", got.PeakBytes)
	}
	if got.SteadyBytes != 300 {
		t.Errorf("SteadyBytes = %d, want 300", got.SteadyBytes)
	}
	if got.Count < len(readings) {
		t.Errorf("Count = %d, want at least %d", got.Count, len(readings))
	}
	if again := s.Stop(); again != got {
		t.Errorf("second Stop = %+v, want the same %+v", again, got)
	}
}

// TestSamplerWithoutProbe proves a plane with nothing to sample reports an
// unknown figure rather than a zero, and that the caller needs no branch
// around the sampler to get there.
func TestSamplerWithoutProbe(t *testing.T) {
	got := NewSampler(time.Second, nil).Stop()
	if got != NoSamples {
		t.Errorf("Stop = %+v, want %+v", got, NoSamples)
	}
}

// TestSamplerDropsUnreadableReadings proves a probe that cannot answer leaves
// the figure unknown instead of recording its -1 as a size.
func TestSamplerDropsUnreadableReadings(t *testing.T) {
	got := NewSampler(time.Millisecond, func() int64 { return -1 }).Stop()
	if got.PeakBytes != -1 || got.SteadyBytes != -1 {
		t.Errorf("Stop = %+v, want both figures unknown", got)
	}
	if got.Count != 0 {
		t.Errorf("Count = %d, want 0 readings taken", got.Count)
	}
}

// TestProcessTreeBytes proves the local probe answers with a resident set on
// the platform that has one, and says it cannot on the platforms that do not
// rather than reporting an engine as free.
func TestProcessTreeBytes(t *testing.T) {
	got := ProcessTreeBytes()
	if runtime.GOOS == "linux" {
		if got <= 0 {
			t.Errorf("ProcessTreeBytes = %d, want the test process's own resident set", got)
		}
		return
	}
	if got != -1 {
		t.Errorf("ProcessTreeBytes = %d, want -1 off Linux", got)
	}
}
