package gen

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/tamnd/graph-bench/target"
)

// graph500Initiator is the standard RMAT initiator (A, B, C, D) the Graph500
// benchmark fixes. A generator config that leaves Initiator at its zero value
// gets these, so the common case reproduces the community-standard skew.
var graph500Initiator = [4]float64{0.57, 0.19, 0.19, 0.05}

// RMAT is the Kronecker / RMAT generator: a graph of N = 2^Scale nodes and
// EdgeFactor*N edges drawn by the Graph500 recursive-quadrant procedure. It is
// the realistic-skew traversal dataset, the one that resembles a real large
// graph's structure (hub-dominated expansions, clustered neighborhoods, a long
// tail of small-degree nodes) while remaining reproducible from two integers and
// four probabilities. Because it is parameterized by Scale it is the natural
// dataset for the flatness check: the same structure at three sizes.
//
// Each edge is placed by recursing Scale times over the adjacency matrix,
// picking one of the four quadrants by the seeded PRNG according to the
// initiator and fixing one source bit and one target bit per level. Self-loops
// and duplicate edges are kept, matching the Graph500 reference; the policy is
// recorded in the manifest params. The edges are sorted by (source, target)
// before emission so the file is in a stable canonical order.
type RMAT struct{}

func (RMAT) Name() string { return "rmat" }
func (RMAT) Version() int { return 1 }

func (g RMAT) Generate(ctx context.Context, cfg Config, w Writer) (*target.Manifest, error) {
	if cfg.Scale <= 0 || cfg.Scale > 30 {
		return nil, fmt.Errorf("rmat: Scale must be in 1..30, got %d", cfg.Scale)
	}
	if cfg.EdgeFactor <= 0 {
		return nil, fmt.Errorf("rmat: EdgeFactor must be > 0, got %d", cfg.EdgeFactor)
	}
	init := cfg.Initiator
	if init == [4]float64{} {
		init = graph500Initiator
	}
	if a, b, c, d := init[0], init[1], init[2], init[3]; a < 0 || b < 0 || c < 0 || d < 0 || abs1(a+b+c+d-1) > 1e-9 {
		return nil, fmt.Errorf("rmat: Initiator must be non-negative and sum to 1, got %v", init)
	}

	n := int64(1) << uint(cfg.Scale)
	edgeCount := int64(cfg.EdgeFactor) * n

	nodes, err := w.NodeFile(nodeLabel, nodeHeader())
	if err != nil {
		return nil, err
	}
	for id := int64(0); id < n; id++ {
		if err := nodes.Write([]string{strconv.FormatInt(id, 10), nodeLabel}); err != nil {
			return nil, err
		}
	}
	if err := nodes.Close(); err != nil {
		return nil, err
	}

	// Cumulative quadrant thresholds, so one PRNG draw selects a quadrant.
	ab := init[0] + init[1]
	abc := ab + init[2]

	prng := NewPRNG(cfg.Seed)
	edges := make([][2]int64, 0, edgeCount)
	for e := int64(0); e < edgeCount; e++ {
		if e%(1<<16) == 0 {
			if err := checkCanceled(ctx); err != nil {
				return nil, err
			}
		}
		var src, dst int64
		for level := 0; level < cfg.Scale; level++ {
			bit := int64(1) << uint(cfg.Scale-1-level)
			r := prng.Float64()
			switch {
			case r < init[0]: // top-left: source bit 0, target bit 0
			case r < ab: // top-right: source bit 0, target bit 1
				dst |= bit
			case r < abc: // bottom-left: source bit 1, target bit 0
				src |= bit
			default: // bottom-right: source bit 1, target bit 1
				src |= bit
				dst |= bit
			}
		}
		edges = append(edges, [2]int64{src, dst})
	}

	sort.Slice(edges, func(i, j int) bool {
		if edges[i][0] != edges[j][0] {
			return edges[i][0] < edges[j][0]
		}
		return edges[i][1] < edges[j][1]
	})

	rels, err := w.RelFile(relType, relHeader())
	if err != nil {
		return nil, err
	}
	for _, e := range edges {
		if err := rels.Write([]string{strconv.FormatInt(e[0], 10), strconv.FormatInt(e[1], 10), relType}); err != nil {
			return nil, err
		}
	}
	if err := rels.Close(); err != nil {
		return nil, err
	}

	m := &target.Manifest{
		Name:             fmt.Sprintf("rmat-s%d-e%d", cfg.Scale, cfg.EdgeFactor),
		Kind:             "synthetic",
		Generator:        g.Name(),
		GeneratorVersion: g.Version(),
		Seed:             cfg.Seed,
		Params: map[string]any{
			"scale":      cfg.Scale,
			"edgeFactor": cfg.EdgeFactor,
			"initiator":  []float64{init[0], init[1], init[2], init[3]},
			"duplicates": "kept",
		},
		Invariants: target.Invariants{NodeCount: i64p(n), EdgeCount: i64p(edgeCount)},
	}
	return w.Finalize(m)
}

// abs1 is the absolute value for the initiator sum check.
func abs1(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
