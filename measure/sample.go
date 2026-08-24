package measure

import (
	"sync"
	"time"
)

// Samples is what a Sampler collected over a run: the largest reading it
// took and the last one, in bytes. Both are -1 when no reading succeeded,
// because a size nobody could read and a size of zero are the same number
// otherwise and the second one reads like a result.
//
// The peak is what an engine has to be provisioned for and the steady
// figure is what it settles at. An engine that reaches a gigabyte to answer
// one query and an engine that holds a gigabyte all day are a different
// problem with the same peak, and a comparison that carries only one of the
// two numbers cannot tell them apart.
type Samples struct {
	PeakBytes   int64
	SteadyBytes int64
	Count       int // readings that succeeded, 0 when the probe never answered
}

// NoSamples is the result of sampling nothing, which is what a plane with no
// probe reports.
var NoSamples = Samples{PeakBytes: -1, SteadyBytes: -1}

// Sampler polls a probe on an interval and keeps the peak and the last
// reading. It is how a figure the kernel keeps no high-water mark for gets a
// peak at all: getrusage answers with ru_maxrss for a process this one
// forked and with nothing for a server in a container, so for that one the
// peak has to be built out of readings taken while it runs.
//
// A probe that cannot answer returns -1 and the reading is dropped rather
// than recorded as a zero. Sampling starts with one reading taken before the
// first tick, so a run shorter than the interval still reports something.
type Sampler struct {
	stop chan struct{}
	done chan struct{}
	once sync.Once

	mu   sync.Mutex
	peak int64
	last int64
	took int
}

// NewSampler starts a Sampler over probe and returns it running. Call Stop once,
// after the engine's session is closed, to end the sampling and read what it
// collected. A nil probe or a non-positive interval gives a sampler that
// takes no readings, so a caller with no probe for its plane does not need a
// branch around this.
func NewSampler(interval time.Duration, probe func() int64) *Sampler {
	s := &Sampler{
		stop: make(chan struct{}),
		done: make(chan struct{}),
		peak: -1,
		last: -1,
	}
	if probe == nil || interval <= 0 {
		close(s.done)
		return s
	}
	s.record(probe())
	go func() {
		defer close(s.done)
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-tick.C:
				s.record(probe())
			}
		}
	}()
	return s
}

// Stop ends the sampling and returns what was collected. It is safe to call
// more than once; later calls return the same result.
func (s *Sampler) Stop() Samples {
	s.once.Do(func() { close(s.stop) })
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return Samples{PeakBytes: s.peak, SteadyBytes: s.last, Count: s.took}
}

// record keeps a reading, ignoring the ones the probe could not answer.
func (s *Sampler) record(v int64) {
	if v < 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v > s.peak {
		s.peak = v
	}
	s.last = v
	s.took++
}
