package gen

import (
	"context"
	"fmt"
	"strconv"

	"github.com/tamnd/graph-bench/target"
)

// Uniform is the k-regular generator: N nodes, every node with out-degree
// exactly Degree, targets drawn uniformly at random with self-loops and
// duplicate targets rejected. It is the control case, the anti-power-law: it
// removes degree skew as a variable so a traversal touches a predictable number
// of neighbors at every hop and the result is the raw k-hop cost without a hub
// blowing up one expansion.
type Uniform struct{}

func (Uniform) Name() string { return "uniform" }
func (Uniform) Version() int { return 1 }

func (g Uniform) Generate(ctx context.Context, cfg Config, w Writer) (*target.Manifest, error) {
	if cfg.N <= 0 {
		return nil, fmt.Errorf("uniform: N must be > 0, got %d", cfg.N)
	}
	if cfg.Degree <= 0 {
		return nil, fmt.Errorf("uniform: Degree must be > 0, got %d", cfg.Degree)
	}
	if int64(cfg.Degree) >= cfg.N {
		return nil, fmt.Errorf("uniform: Degree %d must be < N %d (cannot pick that many distinct targets)", cfg.Degree, cfg.N)
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

	rels, err := w.RelFile(relType, relHeader())
	if err != nil {
		return nil, err
	}
	prng := NewPRNG(cfg.Seed)
	// A small reused set tracks the targets already chosen for the current
	// source, so the k targets are distinct and never the source itself.
	chosen := make(map[int64]struct{}, cfg.Degree)
	var edges int64
	for u := int64(0); u < cfg.N; u++ {
		if err := checkCanceled(ctx); err != nil {
			return nil, err
		}
		for k := range chosen {
			delete(chosen, k)
		}
		for len(chosen) < cfg.Degree {
			v := prng.Int63n(cfg.N)
			if v == u {
				continue
			}
			if _, dup := chosen[v]; dup {
				continue
			}
			chosen[v] = struct{}{}
		}
		// Emit the source's targets in ascending order so the file is stable
		// regardless of the order the set happened to fill.
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
		Name:             fmt.Sprintf("uniform-n%d-k%d", cfg.N, cfg.Degree),
		Kind:             "synthetic",
		Generator:        g.Name(),
		GeneratorVersion: g.Version(),
		Seed:             cfg.Seed,
		Params:           map[string]any{"n": cfg.N, "degree": cfg.Degree},
		Invariants:       target.Invariants{NodeCount: i64p(cfg.N), EdgeCount: i64p(edges)},
	}
	return w.Finalize(m)
}
