package gen

import (
	"context"
	"fmt"
	"math"
	"strconv"

	"github.com/tamnd/graph-bench/target"
)

// PowerLaw is the heavy-tailed generator: N nodes whose out-degrees follow a
// discrete power law with exponent Gamma, floored at MinDeg and clamped at
// MaxDeg. A few hub nodes have very high degree and most nodes sit near MinDeg,
// which is the distribution real graphs (social, web, citation) approximate and
// the one that exposes whether an engine handles a high-degree vertex gracefully
// instead of materializing a huge neighbor list.
//
// Each node's degree is drawn by inverting the power-law cumulative distribution
// over the integer range [MinDeg, MaxDeg], then that many distinct targets are
// drawn uniformly (rejecting self-loops and duplicates). This gives an exact
// power-law out-degree and an exact edge count (the sum of the drawn degrees),
// which is simpler and more directly reproducible than the configuration-model
// stub matching while preserving the structural property that matters here. The
// divergence from the spec's configuration-model sketch is recorded in the
// implementation note.
type PowerLaw struct{}

func (PowerLaw) Name() string { return "powerlaw" }
func (PowerLaw) Version() int { return 1 }

func (g PowerLaw) Generate(ctx context.Context, cfg Config, w Writer) (*target.Manifest, error) {
	if cfg.N <= 0 {
		return nil, fmt.Errorf("powerlaw: N must be > 0, got %d", cfg.N)
	}
	if cfg.Gamma <= 1 {
		return nil, fmt.Errorf("powerlaw: Gamma must be > 1, got %g", cfg.Gamma)
	}
	minDeg, maxDeg := cfg.MinDeg, cfg.MaxDeg
	if minDeg < 1 {
		minDeg = 1
	}
	if maxDeg < minDeg {
		return nil, fmt.Errorf("powerlaw: MaxDeg %d must be >= MinDeg %d", maxDeg, minDeg)
	}
	if int64(maxDeg) >= cfg.N {
		return nil, fmt.Errorf("powerlaw: MaxDeg %d must be < N %d", maxDeg, cfg.N)
	}

	nodes, err := w.NodeFile(nodeLabel, nodeHeader())
	if err != nil {
		return nil, err
	}
	for id := int64(0); id < cfg.N; id++ {
		if err := nodes.Write([]string{strconv.FormatInt(id, 10), nodeLabel}); err != nil {
			return nil, err
		}
	}
	if err := nodes.Close(); err != nil {
		return nil, err
	}

	// Build the cumulative distribution over [minDeg, maxDeg], P(d) proportional
	// to d^-Gamma, once. A degree draw is a single search into this table.
	cdf := powerLawCDF(minDeg, maxDeg, cfg.Gamma)

	rels, err := w.RelFile(relType, relHeader())
	if err != nil {
		return nil, err
	}
	prng := NewPRNG(cfg.Seed)
	chosen := make(map[int64]struct{}, maxDeg)
	var edges int64
	for u := int64(0); u < cfg.N; u++ {
		if err := checkCanceled(ctx); err != nil {
			return nil, err
		}
		deg := drawDegree(cdf, minDeg, prng.Float64())
		for k := range chosen {
			delete(chosen, k)
		}
		for len(chosen) < deg {
			v := prng.Int63n(cfg.N)
			if v == u {
				continue
			}
			if _, dup := chosen[v]; dup {
				continue
			}
			chosen[v] = struct{}{}
		}
		for v := int64(0); v < cfg.N; v++ {
			if _, ok := chosen[v]; !ok {
				continue
			}
			if err := rels.Write([]string{strconv.FormatInt(u, 10), strconv.FormatInt(v, 10), relType}); err != nil {
				return nil, err
			}
			edges++
		}
	}
	if err := rels.Close(); err != nil {
		return nil, err
	}

	m := &target.Manifest{
		Name:             fmt.Sprintf("powerlaw-n%d-g%g", cfg.N, cfg.Gamma),
		Kind:             "synthetic",
		Generator:        g.Name(),
		GeneratorVersion: g.Version(),
		Seed:             cfg.Seed,
		Params:           map[string]any{"n": cfg.N, "gamma": cfg.Gamma, "minDeg": minDeg, "maxDeg": maxDeg},
		Invariants:       target.Invariants{NodeCount: i64p(cfg.N), EdgeCount: i64p(edges)},
	}
	return w.Finalize(m)
}

// powerLawCDF returns the cumulative distribution over degrees minDeg..maxDeg,
// where the probability of degree d is proportional to d^-gamma. cdf[i] is the
// cumulative probability through degree minDeg+i, ending at 1.
func powerLawCDF(minDeg, maxDeg int, gamma float64) []float64 {
	n := maxDeg - minDeg + 1
	cdf := make([]float64, n)
	var sum float64
	for i := 0; i < n; i++ {
		sum += math.Pow(float64(minDeg+i), -gamma)
		cdf[i] = sum
	}
	for i := range cdf {
		cdf[i] /= sum
	}
	return cdf
}

// drawDegree maps a uniform draw in [0,1) to a degree via the CDF: the first
// bucket whose cumulative probability is greater than r.
func drawDegree(cdf []float64, minDeg int, r float64) int {
	for i, c := range cdf {
		if r < c {
			return minDeg + i
		}
	}
	return minDeg + len(cdf) - 1
}
