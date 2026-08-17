// Package zu is the graph-bench adapter for the zu engine, driven
// in-process over libzu (crates/zu-capi): the database opens inside the
// harness process and every query is a direct C call, with no frame, no
// pipe, and no child process in the timed region. That is the plane
// ladybug runs on, so a zu column and a ladybug column in one table
// compare two engines and not two transports.
//
// The session itself is behind the zuinproc build tag, because it needs
// libzu and its header on the machine. This file is the descriptor, and
// it builds everywhere: a binary built without the tag still lists zu,
// and a run against it fails at Start with the build line to use, rather
// than reporting an unknown engine.
//
// There used to be a second adapter here that drove the zu CLI over a
// pipe, one JSON frame per query. It measured the frame more than the
// engine (a flat 11µs to 13µs per query, which is several times some of
// the reads underneath it), and it is gone. The engine binary is still
// needed: libzu has no bulk-load entry point, so Load shells out to
// `zu copy --reorder degree` once, outside every timed region, and the
// discovery order for that binary is Config["bin"] → $ZU_BIN →
// exec.LookPath("zu") → ../zu/target/release/zu → ../zu/target/debug/zu.
package zu

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/tamnd/graph-bench/engine"
)

// relFallbacks are the local-build binary locations tried after PATH.
var relFallbacks = []string{
	"../zu/target/release/zu",
	"../zu/target/debug/zu",
}

// Engine is the zu engine descriptor. Zero value is ready to use.
type Engine struct{}

// New returns the zu engine descriptor. Nothing opens until Start.
func New() *Engine { return &Engine{} }

var _ engine.Engine = (*Engine)(nil)

// Info reports zu's static identity and honest capabilities as of the
// v0.3.0 pin (spec 04 §2). Every false is a dated fact, not a design
// constant. MaxConcurrency stays 1 because a libzu connection is not
// safe to use from two threads at once.
func (e *Engine) Info() engine.Info {
	return engine.Info{
		Name:  "zu",
		Plane: engine.InProc,
		// zuQL only, with no Cypher fallback. zu's parser accepts a Cypher
		// shape for the subset it implements, but it is not Cypher: labels
		// are case-sensitive and the loader names its tables "node" and
		// "edge", so a text written for Neo4j fails on the label alone. A
		// one-dialect chain makes a query with no zuQL text SKIP with reason
		// "no-dialect-text", which is the honest outcome. With Cypher in the
		// chain it would instead FAIL, and one FAIL discards the measurement
		// for every query in the workload.
		Dialects: []engine.Dialect{engine.ZuQL},
		Caps: engine.Capabilities{
			Transactions:   false,
			BulkLoad:       true,
			Deletes:        false,
			VarLengthPaths: true,
			ShortestPaths:  true,
			// The kernels reachable through CALL. louvain is there too
			// but no workload asks for it under that name.
			Algorithms:     []string{"bfs", "pagerank", "wcc", "sssp", "cdlp", "lcc", "tc", "bc"},
			PathPredicates: false,
			MaxConcurrency: 1,
			Persistent:     true,
		},
	}
}

// discoverBinary walks the spec'd discovery order and returns the first
// hit; on failure the error lists everywhere it looked. Only Load needs
// it, for the copy verb.
func discoverBinary(cfg engine.Config) (string, error) {
	var tried []string
	check := func(desc, path string) (string, bool) {
		if path == "" {
			return "", false
		}
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
			return path, true
		}
		tried = append(tried, fmt.Sprintf("%s %q", desc, path))
		return "", false
	}
	if p, ok := check(`config "bin"`, cfg.Get("bin", "")); ok {
		return p, nil
	} else if cfg.Get("bin", "") == "" {
		tried = append(tried, `config "bin" (unset)`)
	}
	if p, ok := check("$ZU_BIN", os.Getenv("ZU_BIN")); ok {
		return p, nil
	} else if os.Getenv("ZU_BIN") == "" {
		tried = append(tried, "$ZU_BIN (unset)")
	}
	if p, err := exec.LookPath("zu"); err == nil {
		return p, nil
	}
	tried = append(tried, `"zu" on $PATH`)
	for _, rel := range relFallbacks {
		if p, ok := check("local build", rel); ok {
			return p, nil
		}
	}
	return "", fmt.Errorf("zu: binary not found; looked at: %s (build zu or set ZU_BIN)",
		strings.Join(tried, ", "))
}

// result is the adapter's engine.Result: a materialized table, since
// libzu hands back a whole result and the harness iterates it row by row.
type result struct {
	cols []string
	rows [][]engine.Value
	idx  int
}

var _ engine.Result = (*result)(nil)

// Columns reports the result column names, in order.
func (r *result) Columns() []string { return r.cols }

// Next advances to the next row and reports whether there was one.
func (r *result) Next() bool {
	if r.idx >= len(r.rows) {
		return false
	}
	r.idx++
	return true
}

// Row returns the current row, valid until the next Next.
func (r *result) Row() []engine.Value { return r.rows[r.idx-1] }

// Err reports the streaming error, of which there is none: the whole
// result was materialized before the first Next.
func (r *result) Err() error { return nil }

// Close releases the result, which the C handle already did.
func (r *result) Close() error { return nil }
