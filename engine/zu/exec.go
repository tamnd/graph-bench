package zu

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/tamnd/graph-bench/engine"
)

// Exec runs one operation, dispatching on the mode probed at Start.
// Mutex-serialized: zu declares MaxConcurrency 1. In query and primitive
// modes the process spawn happens inside Exec by design — that is the
// Subprocess plane's cost (F3), reported via Calibrate, never hidden.
func (s *Session) Exec(ctx context.Context, op engine.Op) (engine.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("zu: session is closed")
	}
	switch s.mode {
	case modeShell:
		return s.execShell(ctx, op)
	case modeQuery:
		return s.execQuery(ctx, op)
	default:
		return s.execPrimitive(ctx, op)
	}
}

// ---- shell mode -----------------------------------------------------

// shellProc is the one persistent `zu shell <db> --format jsonl` child.
type shellProc struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	stderr *bytes.Buffer
}

// startShellLocked lazily starts the persistent shell child. Caller
// holds s.mu.
func (s *Session) startShellLocked() error {
	if s.sh != nil {
		return nil
	}
	cmd := exec.Command(s.bin, "shell", s.dbPath, "--format", "jsonl")
	in, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("zu: shell stdin: %w", err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("zu: shell stdout: %w", err)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("zu: start shell: %w", err)
	}
	s.sh = &shellProc{cmd: cmd, in: in, out: bufio.NewReader(out), stderr: stderr}
	return nil
}

// teardownShellLocked kills the shell child and returns its captured
// stderr. Caller holds s.mu. Safe when no shell is running.
func (s *Session) teardownShellLocked() string {
	if s.sh == nil {
		return ""
	}
	sh := s.sh
	s.sh = nil
	sh.in.Close()
	if sh.cmd.Process != nil {
		sh.cmd.Process.Kill()
	}
	sh.cmd.Wait()
	return strings.TrimSpace(sh.stderr.String())
}

// shellFrame is one request line on the jsonl shell protocol. The
// params travel inside the frame, because every pooled query's text
// names them ($id, $seed, $src) and zu's binder rejects unbound
// parameters; a bare statement line has nowhere to put them.
type shellFrame struct {
	Op     string                  `json:"op"`
	Q      string                  `json:"q"`
	Params map[string]engine.Value `json:"params,omitempty"`
}

// execShell writes one query frame to the persistent shell and reads
// one JSON result line back. On ctx cancellation the child is killed
// (a fresh one starts lazily on the next op).
func (s *Session) execShell(ctx context.Context, op engine.Op) (engine.Result, error) {
	if err := s.startShellLocked(); err != nil {
		return nil, err
	}
	frame, err := json.Marshal(shellFrame{Op: "query", Q: op.Text, Params: op.Params})
	if err != nil {
		return nil, fmt.Errorf("zu: query %q has a parameter JSON cannot carry: %w", op.QueryID, err)
	}
	if _, err := io.WriteString(s.sh.in, string(frame)+"\n"); err != nil {
		stderr := s.teardownShellLocked()
		return nil, fmt.Errorf("zu: shell write failed: %v (stderr: %s)", err, stderr)
	}

	type readReply struct {
		line string
		err  error
	}
	ch := make(chan readReply, 1)
	out := s.sh.out
	go func() {
		line, err := out.ReadString('\n')
		ch <- readReply{line, err}
	}()

	select {
	case <-ctx.Done():
		s.teardownShellLocked() // unblocks the reader goroutine via EOF
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			stderr := s.teardownShellLocked()
			return nil, fmt.Errorf("zu: shell read failed: %v (stderr: %s)", r.err, stderr)
		}
		return decodeJSONResult([]byte(r.line))
	}
}

// ---- query mode -----------------------------------------------------

// execQuery spawns `zu query <db> -c <text> --format json` for one op,
// with one -p flag per parameter.
//
// The parameters are not optional decoration: every pooled query's text
// names them ($id, $seed, $src), and zu's binder rejects a statement
// whose parameters were never bound rather than treating them as null.
// Passing the text alone would fail every pooled query in the workload,
// which is a FAIL, which discards the measurement for all of them.
func (s *Session) execQuery(ctx context.Context, op engine.Op) (engine.Result, error) {
	args := make([]string, 0, 6+2*len(op.Params))
	args = append(args, "query", s.dbPath, "-c", op.Text, "--format", "json")
	// Sorted so the command line is a function of the op alone: the same
	// op produces the same argv on every repetition and every host, which
	// is what makes a failure reproducible from the run log.
	for _, name := range slices.Sorted(maps.Keys(op.Params)) {
		text, ok := formatParam(op.Params[name])
		if !ok {
			return nil, fmt.Errorf("zu: query %q parameter %q is %T, which the CLI cannot carry",
				op.QueryID, name, op.Params[name])
		}
		args = append(args, "-p", name+"="+text)
	}
	cmd := exec.CommandContext(ctx, s.bin, args...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("zu: query %q failed: %v\n%s", op.QueryID, err, bytes.TrimSpace(ee.Stderr))
		}
		return nil, fmt.Errorf("zu: query %q failed: %w", op.QueryID, err)
	}
	return decodeJSONResult(bytes.TrimSpace(out))
}

// ---- primitive mode -------------------------------------------------

var (
	reLookupHit = regexp.MustCompile(`key\s+\S+:\s+node\s+(\S+)`)
	reDegree    = regexp.MustCompile(`(?:in-)?degree\s+([0-9]+)`)
)

// execPrimitive maps micro-read query IDs onto zu CLI primitives.
// Anything outside the mapping errors; the runner pre-filters with
// PrimitiveQueries so those queries SKIP instead.
//
// Result columns carry the names the query's own ZuQL text declares
// ("id", "n", "found"), not zu's CLI wording: the adapter is answering
// that query, so its answer must be shaped like that query's answer or
// validation compares columns that were never meant to line up. The edge
// probe is the case to watch — zu's CLI prints "exists", but the query
// aliases the column "found" because EXISTS is reserved in another engine's
// parser, so the two spellings are deliberately different here.
func (s *Session) execPrimitive(ctx context.Context, op engine.Op) (engine.Result, error) {
	switch op.QueryID {
	case "micro-point", "micro-point-miss":
		id, ok := paramString(op.Params, "id", "seed")
		if !ok {
			return nil, fmt.Errorf("zu: %s needs an \"id\" or \"seed\" param", op.QueryID)
		}
		out, code, err := s.runPrimitive(ctx, "lookup", s.dbPath, id)
		if err != nil {
			return nil, err
		}
		if code == 1 { // miss
			return &result{cols: []string{"id"}}, nil
		}
		// The query is `RETURN n.id AS id`, so the answer is the key the
		// caller asked for, confirmed present — not the internal node id
		// lookup also prints, which is a storage detail no other engine
		// has and which would fail comparison against every one of them.
		if !reLookupHit.MatchString(out) {
			return nil, fmt.Errorf("zu: lookup %s: unparsable output %.120q", id, out)
		}
		return &result{cols: []string{"id"}, rows: [][]engine.Value{{asValue(id)}}}, nil

	case "micro-khop1":
		id, ok := paramString(op.Params, "id", "seed")
		if !ok {
			return nil, fmt.Errorf("zu: %s needs an \"id\" or \"seed\" param", op.QueryID)
		}
		// --key is required: without it zu reads the argument as an
		// internal node id, which after `copy --reorder degree` is a
		// different node than the one the query asked about. It answers
		// (some node's degree) instead of failing, so the flag is the
		// whole difference between a measurement and a coincidence.
		out, code, err := s.runPrimitive(ctx, "neighbors", "--key", s.dbPath, id)
		if err != nil {
			return nil, err
		}
		if code != 0 {
			return nil, fmt.Errorf("zu: neighbors --key %s failed: %s", id, strings.TrimSpace(out))
		}
		m := reDegree.FindStringSubmatch(out)
		if m == nil {
			return nil, fmt.Errorf("zu: neighbors output has no degree header: %.120q", out)
		}
		return &result{
			cols: []string{"n"},
			rows: [][]engine.Value{{atoi64(m[1], 0)}},
		}, nil

	case "micro-edge":
		src, okS := paramString(op.Params, "src", "id", "seed")
		dst, okD := paramString(op.Params, "dst")
		if !okS || !okD {
			return nil, fmt.Errorf(`zu: %s needs "src" and "dst" params`, op.QueryID)
		}
		// zu edge takes internal node ids and has no --key form, so both
		// endpoints are resolved first. That is three spawns for one
		// edge probe, and the spawn floor is never subtracted (F4), so
		// micro-edge reads about three times the floor on this plane.
		// The alternative — loading without --reorder degree to make the
		// keys line up — would trade zu's documented best-practice load
		// for a cheaper number, which is the kind of trade this harness
		// exists to refuse.
		srcID, srcOK, err := s.lookupNodeID(ctx, src)
		if err != nil {
			return nil, err
		}
		dstID, dstOK, err := s.lookupNodeID(ctx, dst)
		if err != nil {
			return nil, err
		}
		if !srcOK || !dstOK {
			// A missing endpoint is a well-defined absent edge.
			return &result{cols: []string{"found"}, rows: [][]engine.Value{{false}}}, nil
		}
		out, code, err := s.runPrimitive(ctx, "edge", s.dbPath, srcID, dstID)
		if err != nil {
			return nil, err
		}
		switch {
		case code == 0 && strings.Contains(out, "exists"):
			return &result{cols: []string{"found"}, rows: [][]engine.Value{{true}}}, nil
		case code == 1 && strings.Contains(out, "absent"):
			return &result{cols: []string{"found"}, rows: [][]engine.Value{{false}}}, nil
		default:
			return nil, fmt.Errorf("zu: edge %s %s failed (exit %d): %s", src, dst, code, strings.TrimSpace(out))
		}

	default:
		return nil, fmt.Errorf("zu: query %q not reachable in primitive mode (zu has no query verb yet; SKIP expected upstream)", op.QueryID)
	}
}

// lookupNodeID resolves a dataset key to zu's internal node id via the
// lookup verb, reporting false when the key is absent. Callers need it
// because only lookup and `neighbors --key` speak keys; the rest of the
// CLI speaks internal ids, which `copy --reorder degree` permutes.
func (s *Session) lookupNodeID(ctx context.Context, key string) (string, bool, error) {
	out, code, err := s.runPrimitive(ctx, "lookup", s.dbPath, key)
	if err != nil {
		return "", false, err
	}
	if code == 1 {
		return "", false, nil
	}
	m := reLookupHit.FindStringSubmatch(out)
	if m == nil {
		return "", false, fmt.Errorf("zu: lookup %s: unparsable output %.120q", key, out)
	}
	return m[1], true, nil
}

// runPrimitive runs one CLI primitive and returns stdout, the exit code,
// and an error for anything other than a clean 0/1 exit. Exit 1 is a
// protocol answer (miss/absent) for lookup and edge, so it is not an
// error here.
func (s *Session) runPrimitive(ctx context.Context, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, s.bin, args...)
	out, err := cmd.Output()
	if err == nil {
		return string(out), 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return string(out), 1, nil
	}
	return "", -1, fmt.Errorf("zu: %s failed: %w", strings.Join(args, " "), err)
}

// paramString extracts the first present parameter under keys, accepting
// string and integer values (int64, int, integral float64).
func paramString(params map[string]engine.Value, keys ...string) (string, bool) {
	for _, k := range keys {
		v, ok := params[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case string:
			return t, true
		case int64:
			return strconv.FormatInt(t, 10), true
		case int:
			return strconv.Itoa(t), true
		case float64:
			if t == math.Trunc(t) {
				return strconv.FormatInt(int64(t), 10), true
			}
		}
	}
	return "", false
}

// formatParam renders one parameter value for a `-p name=value` flag.
// zu types the value by what the text parses as (integer, then float,
// then string), so an int64 arrives as an int and a string as a string
// without any marker in between. A float that happens to be integral is
// written with a decimal point on purpose: dropping it would silently
// change the parameter's type on the other side.
func formatParam(v engine.Value) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case int64:
		return strconv.FormatInt(t, 10), true
	case int:
		return strconv.Itoa(t), true
	case float64:
		s := strconv.FormatFloat(t, 'f', -1, 64)
		if !strings.ContainsAny(s, ".eE") {
			s += ".0"
		}
		return s, true
	default:
		// Booleans deliberately fall through. zu's CLI types a parameter
		// by what it parses as, and "true" parses as a string there, so
		// sending one would bind a string where the query wants a bool
		// and compare wrong. No workload passes one; an error here is a
		// clearer answer than a quiet mistype.
		return "", false
	}
}

// asValue decodes a primitive-output token into the value model:
// int64 when it parses, string otherwise.
func asValue(s string) engine.Value {
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v
	}
	return s
}

// ---- result + JSON decode ------------------------------------------

// result is an in-memory Result: zu's JSON surface returns whole answers
// per statement, so the stream is materialized at decode time. The
// runner still drains it inside the timed region, uniformly with every
// engine.
type result struct {
	cols []string
	rows [][]engine.Value
	idx  int
}

func (r *result) Columns() []string { return r.cols }
func (r *result) Next() bool {
	if r.idx < len(r.rows) {
		r.idx++
		return true
	}
	return false
}
func (r *result) Row() []engine.Value { return r.rows[r.idx-1] }
func (r *result) Err() error          { return nil }
func (r *result) Close() error        { return nil }

// decodeJSONResult decodes one {"columns":[...],"rows":[[...]]} object
// (or {"error":"..."}) into a Result. Numbers decode to int64 when
// integral, float64 otherwise.
func decodeJSONResult(data []byte) (engine.Result, error) {
	var payload struct {
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
		Error   string   `json:"error"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return nil, fmt.Errorf("zu: bad result JSON: %v (line: %.200s)", err, bytes.TrimSpace(data))
	}
	if payload.Error != "" {
		return nil, fmt.Errorf("zu: %s", payload.Error)
	}
	rows := make([][]engine.Value, len(payload.Rows))
	for i, in := range payload.Rows {
		row := make([]engine.Value, len(in))
		for j, v := range in {
			row[j] = convertValue(v)
		}
		rows[i] = row
	}
	return &result{cols: payload.Columns, rows: rows}, nil
}

// convertValue normalizes decoded JSON into the canonical value model.
func convertValue(v any) engine.Value {
	switch t := v.(type) {
	case json.Number:
		if i, err := strconv.ParseInt(string(t), 10, 64); err == nil {
			return i
		}
		f, _ := t.Float64()
		return f
	case []any:
		out := make([]engine.Value, len(t))
		for i, e := range t {
			out[i] = convertValue(e)
		}
		return out
	case map[string]any:
		out := make(map[string]engine.Value, len(t))
		for k, e := range t {
			out[k] = convertValue(e)
		}
		return out
	default:
		return t // nil, bool, string
	}
}
