package gen

import (
	"context"
	"fmt"
	"strconv"

	"github.com/tamnd/graph-bench/target"
)

// Grid is the 2D mesh generator: Rows by Cols cells, each connected to its
// orthogonal neighbors (4-neighbor) and, when Diagonal is set, its diagonal
// neighbors too (8-neighbor). It is the generator with the richest closed-form
// invariants, which is why it is in the set: node count, edge count, diameter,
// every pairwise distance, and the triangle count are all arithmetic, so a
// shortest-path workload validates against a formula rather than a second
// engine. It is also the high-diameter dataset that forces a traversal to go
// deep and exposes per-hop overhead a shallow graph hides.
//
// The structure has no randomness at all; the seed is recorded for uniformity
// but a grid is identical for every seed. Edges are emitted once per undirected
// pair in ascending source-then-target order, fully determined by the
// dimensions.
type Grid struct{}

func (Grid) Name() string { return "grid" }
func (Grid) Version() int { return 1 }

func (g Grid) Generate(ctx context.Context, cfg Config, w Writer) (*target.Manifest, error) {
	if cfg.Rows <= 0 || cfg.Cols <= 0 {
		return nil, fmt.Errorf("grid: Rows and Cols must be > 0, got %dx%d", cfg.Rows, cfg.Cols)
	}
	rows, cols := int64(cfg.Rows), int64(cfg.Cols)
	n := rows * cols

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

	rels, err := w.RelFile(relType, relHeader())
	if err != nil {
		return nil, err
	}
	// id(r, c) = r*cols + c. For each cell, emit edges to the neighbors with a
	// strictly larger id so each undirected pair is written exactly once: right
	// (c+1), down (r+1), and for the 8-neighbor grid the two downward diagonals.
	var edges int64
	emit := func(a, b int64) error {
		edges++
		return rels.Write([]string{strconv.FormatInt(a, 10), strconv.FormatInt(b, 10), relType})
	}
	for r := int64(0); r < rows; r++ {
		if err := checkCanceled(ctx); err != nil {
			return nil, err
		}
		for c := int64(0); c < cols; c++ {
			id := r*cols + c
			if c+1 < cols { // right neighbor
				if err := emit(id, id+1); err != nil {
					return nil, err
				}
			}
			if r+1 < rows { // down neighbor
				if err := emit(id, id+cols); err != nil {
					return nil, err
				}
			}
			if cfg.Diagonal && r+1 < rows {
				if c+1 < cols { // down-right diagonal
					if err := emit(id, id+cols+1); err != nil {
						return nil, err
					}
				}
				if c-1 >= 0 { // down-left diagonal
					if err := emit(id, id+cols-1); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	if err := rels.Close(); err != nil {
		return nil, err
	}

	inv := target.Invariants{NodeCount: i64p(n), EdgeCount: i64p(edges)}
	if !cfg.Diagonal {
		// 4-neighbor grid: diameter is the Manhattan distance corner to corner,
		// and the graph is bipartite so it has no triangles.
		inv.Diameter = i64p((rows - 1) + (cols - 1))
		inv.TriangleCount = i64p(0)
	} else {
		// 8-neighbor grid: diameter is the Chebyshev distance corner to corner.
		diam := rows - 1
		if cols-1 > diam {
			diam = cols - 1
		}
		inv.Diameter = i64p(diam)
	}

	m := &target.Manifest{
		Name:             fmt.Sprintf("grid-%dx%d", cfg.Rows, cfg.Cols),
		Kind:             "synthetic",
		Generator:        g.Name(),
		GeneratorVersion: g.Version(),
		Seed:             cfg.Seed,
		Params:           map[string]any{"rows": cfg.Rows, "cols": cfg.Cols, "diagonal": cfg.Diagonal},
		Invariants:       inv,
	}
	return w.Finalize(m)
}
