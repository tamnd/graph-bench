package gen

// PRNG is the one source of randomness a generator is allowed to use. It is a
// SplitMix64 generator implemented in this package on purpose: the standard
// library's math/rand stream is explicitly not guaranteed stable across Go
// versions, and bit-reproducibility (spec doc 04 section 3) requires a stream
// that is fixed forever. SplitMix64 is small, fast, well-distributed, and its
// output for a given seed is a documented constant, so a dataset generated on
// one machine and Go version reproduces byte-for-byte on another.
//
// The algorithm is part of every generator's reproduction recipe: if this code
// ever changes the stream it produces, the affected generators must bump their
// Version so old datasets stay reproducible by the old code path.
type PRNG struct {
	state uint64
}

// NewPRNG returns a PRNG seeded from seed. The seed is taken as an unsigned
// 64-bit value, so a negative seed is well-defined (its two's-complement bits).
func NewPRNG(seed int64) *PRNG {
	return &PRNG{state: uint64(seed)}
}

// Next returns the next 64-bit value in the stream. This is the SplitMix64
// step: advance the state by the golden-ratio increment, then run the state
// through two xor-shift-multiply mixing rounds.
func (p *PRNG) Next() uint64 {
	p.state += 0x9e3779b97f4a7c15
	z := p.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// Uint64n returns a value uniformly in [0, n) without modulo bias. It uses
// Lemire's multiply-and-shift rejection: the rejection threshold is the part of
// the 64-bit space that does not divide evenly into n buckets. n must be > 0.
func (p *PRNG) Uint64n(n uint64) uint64 {
	if n == 0 {
		panic("gen: Uint64n called with n == 0")
	}
	// Fast path for powers of two: a mask is unbiased and cheap.
	if n&(n-1) == 0 {
		return p.Next() & (n - 1)
	}
	// Lemire: multiply a full-width random into the [0, n) range and reject the
	// low slice that would bias the result.
	threshold := (-n) % n
	for {
		x := p.Next()
		hi, lo := mul64(x, n)
		if lo >= threshold {
			return hi
		}
	}
}

// Int63n returns a non-negative int64 uniformly in [0, n). n must be > 0 and
// fit in a positive int64.
func (p *PRNG) Int63n(n int64) int64 {
	if n <= 0 {
		panic("gen: Int63n called with n <= 0")
	}
	return int64(p.Uint64n(uint64(n)))
}

// Float64 returns a float64 uniformly in [0, 1). It uses the top 53 bits of the
// stream, the number of bits a float64 mantissa can represent exactly, so every
// returned value is a distinct representable double and the distribution is
// uniform over the unit interval.
func (p *PRNG) Float64() float64 {
	return float64(p.Next()>>11) / (1 << 53)
}

// mul64 returns the high and low 64 bits of the 128-bit product x*y, used by the
// unbiased bounded draw. It is the portable schoolbook multiply, so the result
// does not depend on any 128-bit integer support in the toolchain.
func mul64(x, y uint64) (hi, lo uint64) {
	const mask = 0xffffffff
	x0, x1 := x&mask, x>>32
	y0, y1 := y&mask, y>>32

	w0 := x0 * y0
	t := x1*y0 + w0>>32
	w1 := t & mask
	w2 := t >> 32
	w1 += x0 * y1

	hi = x1*y1 + w2 + w1>>32
	lo = x * y
	return hi, lo
}
