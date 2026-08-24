//go:build !linux

package measure

// ProcessTreeBytes reports an unknown resident set everywhere but Linux.
//
// There is a reading to be had on darwin, through proc_pidinfo, but only by
// linking libproc through cgo, and the default build of this harness is
// cgo-free. The other way to it is to fork ps once a second, and that is
// worse than no number: every fork lands in RUSAGE_CHILDREN, which is the
// accounting a subprocess engine's own cost is read out of, so a sampler
// built that way would pay for itself out of the figures it sits beside.
//
// The peak resident rows still answer here, from getrusage. What is missing
// off Linux is the steady state, which no rusage field holds.
func ProcessTreeBytes() int64 { return -1 }
