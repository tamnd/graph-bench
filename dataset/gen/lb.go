package gen

import (
	"context"
	"fmt"
	"strconv"

	"github.com/tamnd/graph-bench/engine"
)

// lbPayloadLen is the payload length for objects and links, mimicking
// LinkBench's ~64-byte median payload.
const lbPayloadLen = 64

// LinkBench-shaped power-law link distribution: exponent 2.0 over [1, 1000],
// the nlinks distribution LinkBench fits from the Facebook social graph.
const (
	lbGamma    = 2.0
	lbMinLinks = 1
	lbMaxLinks = 1000
)

// LB is the LinkBench-shaped generator: one Obj node table and one LINK edge
// table, the object/association model of Facebook's LinkBench (spec 05 §2).
// It exists because upstream LinkBench is a Java load driver, not a dataset:
// this emits the equivalent initial graph so the linkbench workload's
// point-read and association-list queries have data with the right skew.
//
// Shape, deterministic from Config.Seed: N objects (default 10000) with a
// small uniform otype in 1..10, a version counter, a uniform time, and a
// 64-character random payload. Each object's out-link count is power-law
// (exponent 2.0, min 1, max 1000, clamped to N-1); targets are distinct,
// never the source, and emitted in ascending order. Each link carries a
// uniform ltype in 1..10, a time, and its own payload.
type LB struct{}

// Name returns "lb".
func (LB) Name() string { return "lb" }

// Version is the algorithm version.
func (LB) Version() string { return "1" }

// Generate emits the object/association graph. See Generator.Generate.
func (g LB) Generate(ctx context.Context, cfg Config, w Writer) (*engine.Manifest, error) {
	n := cfg.N
	if n == 0 {
		n = 10000
	}
	if n < 2 {
		return nil, fmt.Errorf("lb: N must be >= 2, got %d", n)
	}
	maxLinks := int64(lbMaxLinks)
	if maxLinks > n-1 {
		maxLinks = n - 1
	}

	prng := NewPRNG(cfg.Seed)

	obj, err := w.NodeFile("Obj", []engine.Column{
		{Name: "id", Type: "ID"},
		{Name: "otype", Type: "INT64"},
		{Name: "version", Type: "INT64"},
		{Name: "time", Type: "INT64"},
		{Name: "payload", Type: "STRING"},
	})
	if err != nil {
		return nil, err
	}
	for i := int64(0); i < n; i++ {
		if err := checkCanceled(ctx); err != nil {
			return nil, err
		}
		row := []string{
			strconv.FormatInt(i, 10),
			strconv.FormatInt(1+prng.Int63n(10), 10),
			strconv.FormatInt(prng.Int63n(10), 10),
			strconv.FormatInt(prng.Int63n(1_000_000_000), 10),
			randString(prng, lbPayloadLen),
		}
		if err := obj.Write(row); err != nil {
			return nil, err
		}
	}
	if err := obj.Close(); err != nil {
		return nil, err
	}

	cdf := powerLawCDF(lbMinLinks, int(maxLinks), lbGamma)
	link, err := w.RelFile("LINK", "Obj", "Obj", []engine.Column{
		{Name: "", Type: "START_ID"}, {Name: "", Type: "END_ID"}, {Name: "", Type: "TYPE"},
		{Name: "ltype", Type: "INT64"},
		{Name: "time", Type: "INT64"},
		{Name: "payload", Type: "STRING"},
	})
	if err != nil {
		return nil, err
	}
	var edges int64
	chosen := make(map[int64]struct{}, maxLinks)
	for u := int64(0); u < n; u++ {
		if err := checkCanceled(ctx); err != nil {
			return nil, err
		}
		deg := drawDegree(cdf, lbMinLinks, prng.Float64())
		for k := range chosen {
			delete(chosen, k)
		}
		for int64(len(chosen)) < int64(deg) {
			v := prng.Int63n(n)
			if v == u {
				continue
			}
			if _, dup := chosen[v]; dup {
				continue
			}
			chosen[v] = struct{}{}
		}
		// Ascending targets keep the file stable; the per-link property draws
		// happen in this fixed emission order.
		for v := int64(0); v < n; v++ {
			if _, ok := chosen[v]; !ok {
				continue
			}
			row := []string{
				strconv.FormatInt(u, 10),
				strconv.FormatInt(v, 10),
				"LINK",
				strconv.FormatInt(1+prng.Int63n(10), 10),
				strconv.FormatInt(prng.Int63n(1_000_000_000), 10),
				randString(prng, lbPayloadLen),
			}
			if err := link.Write(row); err != nil {
				return nil, err
			}
			edges++
		}
	}
	if err := link.Close(); err != nil {
		return nil, err
	}

	m := &engine.Manifest{
		Name:             fmt.Sprintf("lb-n%d", n),
		Kind:             "synthetic",
		Generator:        g.Name(),
		GeneratorVersion: g.Version(),
		Seed:             cfg.Seed,
		Scale:            fmt.Sprintf("n%d", n),
		Params: map[string]string{
			"nodes":    iparam(n),
			"gamma":    fparam(lbGamma),
			"minLinks": iparam(lbMinLinks),
			"maxLinks": iparam(maxLinks),
		},
		Invariants: engine.Invariants{NodeCount: n, EdgeCount: edges},
	}
	return w.Finalize(m)
}

// lbCharset is the payload alphabet; 36 symbols so a draw is one bounded PRNG
// step.
const lbCharset = "abcdefghijklmnopqrstuvwxyz0123456789"

// randString draws a fixed-length random string from the payload alphabet.
func randString(prng *PRNG, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = lbCharset[prng.Uint64n(uint64(len(lbCharset)))]
	}
	return string(b)
}
