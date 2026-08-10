// Package zu is the graph-bench adapter for the zu engine, driven over
// the Subprocess plane: it talks to zu the way any user's shell script
// could, through the zu CLI binary. No build tag; pure Go.
//
// Binary discovery order (spec 04 §2): Config["bin"] → $ZU_BIN →
// exec.LookPath("zu") → ../zu/target/release/zu → ../zu/target/debug/zu.
// Start fails loudly, listing every location it tried.
//
// Exec runs in one of three modes, probed at Start from `zu help` and
// confirmed per verb (today's zu lists "shell" and "query" in help while
// answering "unknown command", so the probe runs each candidate verb
// bare and only trusts it when it does not reply "unknown command"):
//
//   - shell mode: one persistent `zu shell <db> --format jsonl` child for
//     the whole session; one statement per line in, one JSON result
//     object per line out. No spawn cost in any timed region.
//   - query mode: `zu query <db> -c <text> --format json` per operation.
//     Process spawn is part of the measured cost — that IS the plane
//     (F3); Session.Calibrate reports the spawn floor so the runner can
//     stamp it alongside, never subtract it.
//   - primitive mode (today's zu): a fixed mapping from micro-read query
//     IDs to CLI primitives — "micro-point"/"micro-point-miss" →
//     `zu lookup`, "micro-khop1" → `zu neighbors`, "micro-edge" →
//     `zu edge`. Only those IDs run; anything else errors.
//
// The runner should pre-filter with PrimitiveQueries / (*Engine).CanRun
// when the session reports Mode() == "primitive", so unmapped queries
// SKIP (reason zu-no-query-verb) instead of erroring mid-run.
package zu

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// Exec modes, probed at Start.
const (
	modeShell     = "shell"
	modeQuery     = "query"
	modePrimitive = "primitive"
)

// relFallbacks are the local-build binary locations tried after PATH.
var relFallbacks = []string{
	"../zu/target/release/zu",
	"../zu/target/debug/zu",
}

// Engine is the zu engine descriptor. Zero value is ready to use.
type Engine struct{}

// New returns the zu engine descriptor.
func New() *Engine { return &Engine{} }

// Info reports zu's static identity and honest capabilities as of the
// v0.3.0 pin (spec 04 §2). Every false is a dated fact, not a design
// constant.
func (e *Engine) Info() engine.Info {
	return engine.Info{
		Name:     "zu",
		Plane:    engine.Subprocess,
		Dialects: []engine.Dialect{engine.ZuQL, engine.Cypher},
		Caps: engine.Capabilities{
			Transactions:   false,
			BulkLoad:       true,
			Deletes:        false,
			VarLengthPaths: true,
			ShortestPaths:  false,
			PathPredicates: false,
			MaxConcurrency: 1,
			Persistent:     true,
		},
	}
}

// PrimitiveQueries lists the workload query IDs reachable in primitive
// mode. The runner uses it to pre-filter when Mode() == "primitive":
// everything else SKIPs with reason "zu-no-query-verb".
func PrimitiveQueries() []string {
	return []string{"micro-point", "micro-point-miss", "micro-khop1", "micro-edge"}
}

// PrimitiveQueries is the method form of the package-level helper.
func (e *Engine) PrimitiveQueries() []string { return PrimitiveQueries() }

// CanRun reports whether queryID is reachable in primitive mode — the
// worst-case exec surface. In shell or query mode every dialect-resolved
// query runs and this filter does not apply.
func (e *Engine) CanRun(queryID string) bool {
	return slices.Contains(PrimitiveQueries(), queryID)
}

// Start discovers the zu binary, probes the exec mode from `zu help`,
// and binds a session to a database file: Config["path"] if given, else
// "bench.zu1" in a fresh temp dir (removed on Close).
func (e *Engine) Start(ctx context.Context, cfg engine.Config) (engine.Session, error) {
	bin, err := discoverBinary(cfg)
	if err != nil {
		return nil, err
	}
	mode, err := probeMode(ctx, bin)
	if err != nil {
		return nil, err
	}
	workDir, err := os.MkdirTemp("", "graph-bench-zu-")
	if err != nil {
		return nil, fmt.Errorf("zu: create work dir: %w", err)
	}
	dbPath := cfg.Get("path", "")
	if dbPath == "" {
		dbPath = filepath.Join(workDir, "bench.zu1")
	}
	return &Session{bin: bin, mode: mode, dbPath: dbPath, workDir: workDir}, nil
}

// discoverBinary walks the spec'd discovery order and returns the first
// hit; on failure the error lists everywhere it looked.
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

// probeMode runs `zu help` and picks the best available exec mode.
// A verb listed in help is only believed after running it bare confirms
// it is implemented: today's zu advertises shell and query in help but
// answers "unknown command" until their milestones land.
func probeMode(ctx context.Context, bin string) (string, error) {
	out, err := exec.CommandContext(ctx, bin, "help").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("zu: %q help failed: %v (output: %s)", bin, err, bytes.TrimSpace(out))
	}
	help := string(out)
	for _, verb := range []string{modeShell, modeQuery} {
		if strings.Contains(help, verb) && verbImplemented(ctx, bin, verb) {
			return verb, nil
		}
	}
	return modePrimitive, nil
}

// verbImplemented runs the verb bare (stdin at EOF, so an implemented
// interactive verb exits immediately on missing args or empty input) and
// reports whether zu recognizes it.
func verbImplemented(ctx context.Context, bin, verb string) bool {
	out, _ := exec.CommandContext(ctx, bin, verb).CombinedOutput()
	return !strings.Contains(string(out), "unknown command")
}

// Session is a live zu session. All Exec calls are mutex-serialized
// (Capabilities.MaxConcurrency == 1).
type Session struct {
	bin     string
	mode    string
	dbPath  string
	workDir string

	mu     sync.Mutex
	sh     *shellProc // lazily started persistent shell (shell mode)
	closed bool
}

// Mode reports the exec mode probed at Start: "shell", "query", or
// "primitive". Stamped into the run condition by the runner.
func (s *Session) Mode() string { return s.mode }

// Bin reports the discovered binary path, for the condition stamp.
func (s *Session) Bin() string { return s.bin }

// Version reports the live version from `zu --version` ("zu X.Y.Z"),
// never from a pin.
func (s *Session) Version(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, s.bin, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("zu: --version failed: %w", err)
	}
	v := strings.TrimSpace(string(out))
	return strings.TrimPrefix(v, "zu "), nil
}

// Begin always fails: zu has no transactions (Capabilities.Transactions
// is false and the runner filters writes out before they reach us).
func (s *Session) Begin(ctx context.Context, mode engine.AccessMode) (engine.Tx, error) {
	return nil, engine.ErrNoTransactions
}

// Calibrate measures the per-operation process-spawn floor of the
// current mode: in query and primitive modes it times five no-op
// `zu --version` invocations and returns the median. The runner stamps
// this alongside results (spec 08 §8) — it is reported, never
// subtracted (F3: the plane is the plane). Shell mode has no per-op
// spawn, so it returns 0. Best effort: 0 on any error.
func (s *Session) Calibrate(ctx context.Context) time.Duration {
	if s.mode == modeShell {
		return 0
	}
	durs := make([]time.Duration, 0, 5)
	for range 5 {
		start := time.Now()
		if err := exec.CommandContext(ctx, s.bin, "--version").Run(); err != nil {
			return 0
		}
		durs = append(durs, time.Since(start))
	}
	slices.Sort(durs)
	return durs[len(durs)/2]
}

// Close kills the persistent shell child (if any) and removes the
// session's temp dir. Idempotent, safe on a partially failed session.
func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.teardownShellLocked()
	if s.workDir != "" {
		os.RemoveAll(s.workDir)
	}
	return nil
}
