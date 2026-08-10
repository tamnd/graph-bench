package gen

import (
	"context"
	"fmt"
	"strconv"

	"github.com/tamnd/graph-bench/engine"
)

// defaultEdgeFactor is the GAP benchmark's edge factor: 16 edges per node.
const defaultEdgeFactor = 16

// URand is the GAP uniform-random generator: a graph of N = 2^Scale nodes and
// EdgeFactor*N edges whose endpoints are drawn independently and uniformly.
// It is rmat's sibling with an unbiased edge distribution — two generators,
// one Writer, per GAP's kron/urand pairing (spec 05 §2) — so a kernel's
// behavior on skew can be separated from its behavior on volume. Self-loops
// and duplicate edges are kept, matching the GAP reference generator.
//
// Every edge carries an int64 weight property w drawn uniformly in 1..255,
// the GAP rule for the SSSP kernel; unweighted kernels simply ignore it. Per
// edge the draw order is source, target, weight, and the edges are sorted by
// (source, target, weight) before emission so the file is in a stable
// canonical order.
type URand struct{}

// Name returns "urand".
func (URand) Name() string { return "urand" }

// Version is the algorithm version.
func (URand) Version() string { return "1" }

// Generate emits the uniform-random graph. See Generator.Generate.
func (g URand) Generate(ctx context.Context, cfg Config, w Writer) (*engine.Manifest, error) {
	if cfg.Scale <= 0 || cfg.Scale > 30 {
		return nil, fmt.Errorf("urand: Scale must be in 1..30, got %d", cfg.Scale)
	}
	ef := cfg.EdgeFactor
	if ef == 0 {
		ef = defaultEdgeFactor
	}
	if ef < 0 {
		return nil, fmt.Errorf("urand: EdgeFactor must be > 0, got %d", ef)
	}

	n := int64(1) << uint(cfg.Scale)
	edgeCount := int64(ef) * n

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

	prng := NewPRNG(cfg.Seed)
	edges := make([]wedge, 0, edgeCount)
	for e := int64(0); e < edgeCount; e++ {
		if e%(1<<16) == 0 {
			if err := checkCanceled(ctx); err != nil {
				return nil, err
			}
		}
		edges = append(edges, wedge{
			src: prng.Int63n(n),
			dst: prng.Int63n(n),
			w:   1 + prng.Int63n(255),
		})
	}
	sortWedges(edges)

	header := append(relHeader(), engine.Column{Name: "w", Type: "INT64"})
	rels, err := w.RelFile(relType, nodeLabel, nodeLabel, header)
	if err != nil {
		return nil, err
	}
	for _, e := range edges {
		row := []string{strconv.FormatInt(e.src, 10), strconv.FormatInt(e.dst, 10), relType, strconv.FormatInt(e.w, 10)}
		if err := rels.Write(row); err != nil {
			return nil, err
		}
	}
	if err := rels.Close(); err != nil {
		return nil, err
	}

	m := &engine.Manifest{
		Name:             fmt.Sprintf("urand-s%d-e%d", cfg.Scale, ef),
		Kind:             "synthetic",
		Generator:        g.Name(),
		GeneratorVersion: g.Version(),
		Seed:             cfg.Seed,
		Scale:            fmt.Sprintf("s%d-e%d", cfg.Scale, ef),
		Params: map[string]string{
			"scale":      iparam(cfg.Scale),
			"edgeFactor": iparam(ef),
			"duplicates": "kept",
		},
		Invariants: engine.Invariants{NodeCount: n, EdgeCount: edgeCount},
	}
	return w.Finalize(m)
}
