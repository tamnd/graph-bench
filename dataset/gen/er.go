package gen

import (
	"context"
	"fmt"
	"math"
	"strconv"

	"github.com/tamnd/graph-bench/engine"
)

// ER is the Erdos-Renyi G(n,p) generator: N nodes, every ordered pair (u, v)
// with u != v an edge independently with probability P. It is the random
// baseline with no structure at all, no community, no hub, no locality, so it
// stresses an engine where the optimizer's structural assumptions do not
// hold, and it is the cleanest triangle-counting fixture because the expected
// triangle count is a simple closed form.
//
// Iterating all N^2 pairs is wasteful for small p, so this uses the standard
// geometric-skip method: the gap to the next edge is a geometric random
// variable with parameter p, and the pair space is walked in a fixed linear
// order. This produces the identical edge set as the naive method for the
// same seed but in time proportional to the edge count rather than N^2, and
// the edges come out in the fixed linear order so the file is stable.
type ER struct{}

// Name returns "er".
func (ER) Name() string { return "er" }

// Version is the algorithm version; the emitted bytes are identical to v1's
// version 1.
func (ER) Version() string { return "1" }

// Generate emits the G(n,p) graph. See Generator.Generate.
func (g ER) Generate(ctx context.Context, cfg Config, w Writer) (*engine.Manifest, error) {
	if cfg.N <= 0 {
		return nil, fmt.Errorf("er: N must be > 0, got %d", cfg.N)
	}
	if cfg.P <= 0 || cfg.P >= 1 {
		return nil, fmt.Errorf("er: P must be in (0,1), got %g", cfg.P)
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

	rels, err := w.RelFile(relType, nodeLabel, nodeLabel, relHeader())
	if err != nil {
		return nil, err
	}
	prng := NewPRNG(cfg.Seed)
	// Walk the N*N linear pair index. log1p of -p is computed once; each step
	// draws a geometric gap to the next candidate pair. Diagonal pairs
	// (u == v) are skipped as candidates, which matches G(n,p) over ordered
	// distinct pairs while keeping the linear walk simple and reproducible.
	logQ := math.Log(1 - cfg.P)
	total := cfg.N * cfg.N
	var edges int64
	idx := int64(-1)
	for {
		r := prng.Float64()
		// gap >= 1; floor(log(1-r)/log(1-p)) is the number of failures before
		// the next success in a geometric(p) sequence.
		gap := int64(math.Floor(math.Log(1-r)/logQ)) + 1
		idx += gap
		if idx >= total {
			break
		}
		if err := checkCanceled(ctx); err != nil {
			return nil, err
		}
		u := idx / cfg.N
		v := idx % cfg.N
		if u == v {
			continue
		}
		if err := rels.Write([]string{strconv.FormatInt(u, 10), strconv.FormatInt(v, 10), relType}); err != nil {
			return nil, err
		}
		edges++
	}
	if err := rels.Close(); err != nil {
		return nil, err
	}

	m := &engine.Manifest{
		Name:             fmt.Sprintf("er-n%d-p%g", cfg.N, cfg.P),
		Kind:             "synthetic",
		Generator:        g.Name(),
		GeneratorVersion: g.Version(),
		Seed:             cfg.Seed,
		Scale:            fmt.Sprintf("n%d-p%g", cfg.N, cfg.P),
		Params:           map[string]string{"n": iparam(cfg.N), "p": fparam(cfg.P)},
		Invariants:       engine.Invariants{NodeCount: cfg.N, EdgeCount: edges},
	}
	return w.Finalize(m)
}
