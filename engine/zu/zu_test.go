package zu

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// The tests drive the adapter against fake zu binaries: /bin/sh scripts
// emulating the CLI surface verified against zu 0.0.1 (2026-08-10).

// fakePrimitive mirrors today's real zu: help advertises shell and query
// but both answer "unknown command"; only the CLI primitives work.
const fakePrimitive = `cmd="$1"
case "$cmd" in
--version) echo "zu 9.9.9-test" ;;
help)
  echo "zu 9.9.9-test: embedded property-graph database"
  echo "commands: shell, query, copy, convert, verify, stat, neighbors [--in] [--key], edge [--in], lookup, bench"
  ;;
shell|query)
  echo "zu: unknown command '$cmd' (commands arrive with their milestones)" 1>&2
  exit 1
  ;;
copy)
  edges="$4"; out="$5"
  n=$(wc -l < "$edges" | tr -d ' ')
  printf 'zu1data' > "$out"
  echo "copied $n edges, 7 nodes, 2 groups"
  echo "parse 0.10s, encode+write 0.15s, total 0.25s"
  echo "1.25 M edges/s end to end, 4096 bytes on disk, 12.50 bits/edge fwd, 10.00 bits/edge bwd"
  ;;
lookup)
  # lookup speaks dataset keys and prints the internal node id that
  # "copy --reorder degree" assigned. Keys 404 and 999 are absent.
  key="$3"
  case "$key" in
    404|999) echo "key $key: absent"; exit 1 ;;
  esac
  echo "key $key: node $((key + 40))"
  ;;
neighbors)
  # Without --key the argument is an internal node id, which is a
  # different node than the caller's key. The real zu answers anyway;
  # this stub refuses, so an adapter that drops --key fails loudly here
  # instead of silently reporting some other node's degree.
  if [ "$2" != "--key" ]; then
    echo "zu-stub: neighbors without --key would read internal node id '$2'" 1>&2
    exit 2
  fi
  key="$4"
  echo "key $key -> node $((key + 40)): degree 3"
  echo 41; echo 42; echo 43
  ;;
edge)
  # edge speaks internal node ids only: there is no --key form. Ids
  # below 40 are keys that were never resolved, i.e. an adapter bug.
  src="$3"; dst="$4"
  if [ "$src" -lt 40 ] || [ "$dst" -lt 40 ]; then
    echo "zu-stub: edge got unresolved key(s) $src $dst" 1>&2
    exit 2
  fi
  echo "$src -> $dst: exists"
  ;;
*) echo "zu: unknown command '$cmd'" 1>&2; exit 1 ;;
esac
`

// fakeQuery implements the spec'd `zu query -c <text> --format json`.
// Help does not mention shell, so probing lands on query mode.
const fakeQuery = `cmd="$1"
case "$cmd" in
--version) echo "zu 9.9.9-test" ;;
help) echo "commands: query, copy, verify, stat, neighbors, edge, lookup" ;;
query)
  if [ $# -lt 2 ]; then echo "usage: zu query <file.zu1> -c <text> [--format json|csv]" 1>&2; exit 1; fi
  echo '{"columns":["a","b"],"rows":[[1,"x"],[2.5,null]]}'
  ;;
*) echo "zu: unknown command '$cmd'" 1>&2; exit 1 ;;
esac
`

// fakeShell implements the spec'd persistent `zu shell --format jsonl`:
// a while-read loop answering one JSON line per statement, with an
// incrementing counter proving one process serves the whole session.
const fakeShell = `cmd="$1"
case "$cmd" in
--version) echo "zu 9.9.9-test" ;;
help) echo "commands: shell, query, copy, verify, stat" ;;
shell)
  if [ $# -lt 2 ]; then echo "usage: zu shell <file.zu1> [--format jsonl]" 1>&2; exit 1; fi
  i=0
  while IFS= read -r line; do
    case "$line" in
    *BAD*) echo '{"error":"parse error near BAD"}' ;;
    *'"src":'*)
      # Answers with the bound $src value, proving the parameter
      # traveled inside the frame and not just the statement text.
      v=$(printf '%s' "$line" | sed 's/.*"src"://; s/[,}].*//')
      echo "{\"columns\":[\"src\"],\"rows\":[[$v]]}"
      ;;
    *) i=$((i+1)); echo "{\"columns\":[\"i\"],\"rows\":[[$i]]}" ;;
    esac
  done
  ;;
query) echo "zu: unknown command 'query' (commands arrive with their milestones)" 1>&2; exit 1 ;;
*) echo "zu: unknown command '$cmd'" 1>&2; exit 1 ;;
esac
`

func requireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake zu binaries are /bin/sh scripts")
	}
}

func writeFake(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "zu")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func start(t *testing.T, bin string) *Session {
	t.Helper()
	s, err := New().Start(context.Background(), engine.Config{
		Values: map[string]string{"bin": bin},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	return s.(*Session)
}

func TestDiscoveryFailure(t *testing.T) {
	t.Setenv("ZU_BIN", "")
	t.Setenv("PATH", t.TempDir()) // empty dir: no zu on PATH
	_, err := New().Start(context.Background(), engine.Config{
		Values: map[string]string{"bin": filepath.Join(t.TempDir(), "no-such-zu")},
	})
	if err == nil {
		t.Fatal("Start succeeded with no binary anywhere")
	}
	for _, want := range []string{`config "bin"`, "ZU_BIN", "PATH", "target/release/zu", "target/debug/zu"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("discovery error missing %q:\n%v", want, err)
		}
	}
}

func TestModeProbing(t *testing.T) {
	requireUnix(t)
	cases := []struct {
		name, script, want string
	}{
		{"primitive-today", fakePrimitive, "primitive"}, // help lists shell/query, both unknown
		{"query", fakeQuery, "query"},
		{"shell", fakeShell, "shell"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := start(t, writeFake(t, tc.script))
			if s.Mode() != tc.want {
				t.Fatalf("mode = %q, want %q", s.Mode(), tc.want)
			}
		})
	}
}

func TestVersion(t *testing.T) {
	requireUnix(t)
	s := start(t, writeFake(t, fakePrimitive))
	v, err := s.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != "9.9.9-test" {
		t.Fatalf("version = %q, want 9.9.9-test", v)
	}
}

type fileDataset struct {
	dir     string
	relFile string
}

func (d *fileDataset) Name() string               { return "test-ds" }
func (d *fileDataset) Checksum() string           { return "sha256:test" }
func (d *fileDataset) Dir() string                { return d.dir }
func (d *fileDataset) Manifest() *engine.Manifest { return nil }
func (d *fileDataset) Schema() engine.Schema {
	return engine.Schema{Rels: map[string]engine.RelSchema{
		"EDGE": {Files: []string{"rels/EDGE.csv"}, Start: "Node", End: "Node"},
	}}
}
func (d *fileDataset) NodeFiles(string) ([]string, error)               { return nil, nil }
func (d *fileDataset) RelFiles(string) ([]string, error)                { return []string{d.relFile}, nil }
func (d *fileDataset) Params(string) ([]map[string]engine.Value, error) { return nil, nil }
func (d *fileDataset) Statements() []string                             { return nil }

func TestLoadStatsParsing(t *testing.T) {
	requireUnix(t)
	dir := t.TempDir()
	relFile := filepath.Join(dir, "EDGE.csv")
	csv := ":START_ID,:END_ID,:TYPE\n0,1,EDGE\n1,2,EDGE\n2,3,EDGE\n0,2,EDGE\n"
	if err := os.WriteFile(relFile, []byte(csv), 0o644); err != nil {
		t.Fatal(err)
	}

	s := start(t, writeFake(t, fakePrimitive))
	stats, err := s.Load(context.Background(), &fileDataset{dir: dir, relFile: relFile})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stats.Method != "copy" {
		t.Errorf("Method = %q, want copy", stats.Method)
	}
	if stats.Edges != 4 { // header skipped, 4 data rows
		t.Errorf("Edges = %d, want 4", stats.Edges)
	}
	if stats.Nodes != 7 {
		t.Errorf("Nodes = %d, want 7", stats.Nodes)
	}
	if stats.BytesOnDisk != 4096 {
		t.Errorf("BytesOnDisk = %d, want 4096", stats.BytesOnDisk)
	}
	if stats.Duration != 250*time.Millisecond { // "total 0.25s"
		t.Errorf("Duration = %v, want 250ms", stats.Duration)
	}
	if _, err := os.Stat(s.dbPath); err != nil {
		t.Errorf("db file not produced: %v", err)
	}
}

func TestLoadStatementsUnsupportedInPrimitiveMode(t *testing.T) {
	requireUnix(t)
	s := start(t, writeFake(t, fakePrimitive))
	ds := engine.NewStatements("micro-write", engine.Schema{}, []string{"CREATE (:A)"})
	_, err := s.Load(context.Background(), ds)
	if err == nil || !strings.Contains(err.Error(), "statements load requires zu query support") {
		t.Fatalf("want statements-load error, got %v", err)
	}
}

func TestPrimitiveExec(t *testing.T) {
	requireUnix(t)
	ctx := context.Background()
	s := start(t, writeFake(t, fakePrimitive))

	t.Run("micro-point hit", func(t *testing.T) {
		res, err := s.Exec(ctx, engine.Op{QueryID: "micro-point",
			Params: map[string]engine.Value{"id": int64(7)}})
		if err != nil {
			t.Fatal(err)
		}
		defer res.Close()
		if !res.Next() {
			t.Fatal("want one row")
		}
		// micro-point is `RETURN n.id AS id`, so the answer is the key
		// asked for (7), confirmed present — not the internal node id
		// (47) that lookup also prints and that no other engine has.
		if got := res.Row()[0]; got != int64(7) {
			t.Fatalf("row = %#v, want int64(7) (the key, not the node id)", got)
		}
		if cols := res.Columns(); len(cols) != 1 || cols[0] != "id" {
			t.Fatalf("columns = %v, want [id] to match the query's ZuQL text", cols)
		}
		if res.Next() {
			t.Fatal("want exactly one row")
		}
	})

	t.Run("micro-point-miss", func(t *testing.T) {
		res, err := s.Exec(ctx, engine.Op{QueryID: "micro-point-miss",
			Params: map[string]engine.Value{"seed": "404"}}) // string param form
		if err != nil {
			t.Fatal(err)
		}
		defer res.Close()
		if res.Next() {
			t.Fatal("miss must yield an empty result")
		}
	})

	t.Run("micro-khop1", func(t *testing.T) {
		res, err := s.Exec(ctx, engine.Op{QueryID: "micro-khop1",
			Params: map[string]engine.Value{"id": int64(5)}})
		if err != nil {
			t.Fatal(err)
		}
		defer res.Close()
		if !res.Next() || res.Row()[0] != int64(3) {
			t.Fatalf("want row [3], got %#v", res.Row())
		}
	})

	t.Run("micro-edge", func(t *testing.T) {
		res, err := s.Exec(ctx, engine.Op{QueryID: "micro-edge",
			Params: map[string]engine.Value{"src": int64(1), "dst": int64(2)}})
		if err != nil {
			t.Fatal(err)
		}
		defer res.Close()
		if !res.Next() || res.Row()[0] != true {
			t.Fatalf("want row [true], got %#v", res.Row())
		}

		res, err = s.Exec(ctx, engine.Op{QueryID: "micro-edge",
			Params: map[string]engine.Value{"src": int64(1), "dst": int64(999)}})
		if err != nil {
			t.Fatal(err)
		}
		defer res.Close()
		if !res.Next() || res.Row()[0] != false {
			t.Fatalf("want row [false], got %#v", res.Row())
		}
	})

	t.Run("unmapped query id", func(t *testing.T) {
		_, err := s.Exec(ctx, engine.Op{QueryID: "is3"})
		if err == nil || !strings.Contains(err.Error(), "primitive mode") {
			t.Fatalf("want primitive-mode error, got %v", err)
		}
	})
}

func TestQueryModeExec(t *testing.T) {
	requireUnix(t)
	s := start(t, writeFake(t, fakeQuery))
	if s.Mode() != "query" {
		t.Fatalf("mode = %q, want query", s.Mode())
	}
	res, err := s.Exec(context.Background(), engine.Op{QueryID: "q1", Text: "MATCH (n) RETURN n.a, n.b"})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	if cols := res.Columns(); len(cols) != 2 || cols[0] != "a" || cols[1] != "b" {
		t.Fatalf("columns = %v", cols)
	}
	if !res.Next() {
		t.Fatal("want row 1")
	}
	if r := res.Row(); r[0] != int64(1) || r[1] != "x" {
		t.Fatalf("row 1 = %#v, want [int64(1) x]", r)
	}
	if !res.Next() {
		t.Fatal("want row 2")
	}
	if r := res.Row(); r[0] != float64(2.5) || r[1] != nil {
		t.Fatalf("row 2 = %#v, want [2.5 <nil>]", r)
	}
	if res.Next() {
		t.Fatal("want exactly two rows")
	}
}

func TestShellModeRoundTrip(t *testing.T) {
	requireUnix(t)
	ctx := context.Background()
	s := start(t, writeFake(t, fakeShell))
	if s.Mode() != "shell" {
		t.Fatalf("mode = %q, want shell", s.Mode())
	}

	exec1 := func(text string) engine.Value {
		t.Helper()
		res, err := s.Exec(ctx, engine.Op{QueryID: "q", Text: text})
		if err != nil {
			t.Fatalf("Exec(%q): %v", text, err)
		}
		defer res.Close()
		if !res.Next() {
			t.Fatalf("Exec(%q): no row", text)
		}
		return res.Row()[0]
	}

	// The fake shell counts statements: same counter across Execs proves
	// one persistent child serves the session.
	if got := exec1("MATCH (n) RETURN count(n)"); got != int64(1) {
		t.Fatalf("first exec = %#v, want 1", got)
	}
	// A statement with embedded newline and tab must fold to one line.
	if got := exec1("MATCH (n)\n\tRETURN n"); got != int64(2) {
		t.Fatalf("second exec = %#v, want 2", got)
	}
	// Parameters ride inside the frame; the fake answers with the bound
	// value, so a pass here means the binder on the other side saw it.
	res, err := s.Exec(ctx, engine.Op{
		QueryID: "q",
		Text:    "MATCH (n {id: $src}) RETURN n.id",
		Params:  map[string]engine.Value{"src": int64(41)},
	})
	if err != nil {
		t.Fatalf("Exec with params: %v", err)
	}
	if !res.Next() || res.Row()[0] != int64(41) {
		t.Fatalf("param round trip = %#v, want 41", res.Row())
	}
	res.Close()

	// An {"error": ...} line surfaces as an error without killing the child.
	if _, err := s.Exec(ctx, engine.Op{QueryID: "q", Text: "BAD ("}); err == nil ||
		!strings.Contains(err.Error(), "parse error") {
		t.Fatalf("want parse error, got %v", err)
	}
	if got := exec1("RETURN 1"); got != int64(3) {
		t.Fatalf("post-error exec = %#v, want 3 (same child)", got)
	}

	if err := s.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(ctx); err != nil { // idempotent
		t.Fatalf("second Close: %v", err)
	}
	if _, err := s.Exec(ctx, engine.Op{QueryID: "q", Text: "RETURN 1"}); err == nil {
		t.Fatal("Exec after Close must fail")
	}
}

func TestCalibrate(t *testing.T) {
	requireUnix(t)
	prim := start(t, writeFake(t, fakePrimitive))
	if d := prim.Calibrate(context.Background()); d <= 0 {
		t.Errorf("primitive-mode Calibrate = %v, want > 0", d)
	}
	sh := start(t, writeFake(t, fakeShell))
	if d := sh.Calibrate(context.Background()); d != 0 {
		t.Errorf("shell-mode Calibrate = %v, want 0", d)
	}
}

func TestPrimitiveQueriesHelper(t *testing.T) {
	e := New()
	want := []string{"micro-point", "micro-point-miss", "micro-khop1", "micro-edge"}
	got := e.PrimitiveQueries()
	if len(got) != len(want) {
		t.Fatalf("PrimitiveQueries = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("PrimitiveQueries = %v, want %v", got, want)
		}
		if !e.CanRun(want[i]) {
			t.Errorf("CanRun(%q) = false", want[i])
		}
	}
	if e.CanRun("bi14") {
		t.Error(`CanRun("bi14") = true, want false`)
	}
}

// propDataset is one rel table with typed property columns, which is the
// shape whose properties travel with the edges.
type propDataset struct {
	dir     string
	relFile string
	props   []engine.Column
	rels    int
}

func (d *propDataset) Name() string               { return "prop-ds" }
func (d *propDataset) Checksum() string           { return "sha256:test" }
func (d *propDataset) Dir() string                { return d.dir }
func (d *propDataset) Manifest() *engine.Manifest { return nil }
func (d *propDataset) Schema() engine.Schema {
	rels := map[string]engine.RelSchema{
		"LINK": {Files: []string{"rels/LINK.csv"}, Start: "Obj", End: "Obj", Properties: d.props},
	}
	if d.rels > 1 {
		rels["OWNS"] = engine.RelSchema{Files: []string{"rels/OWNS.csv"}, Start: "Obj", End: "Obj"}
	}
	return engine.Schema{Rels: rels}
}
func (d *propDataset) NodeFiles(string) ([]string, error)               { return nil, nil }
func (d *propDataset) RelFiles(string) ([]string, error)                { return []string{d.relFile}, nil }
func (d *propDataset) Params(string) ([]map[string]engine.Value, error) { return nil, nil }
func (d *propDataset) Statements() []string                             { return nil }

const linkCSV = ":START_ID,:END_ID,:TYPE,ltype:INT64,payload:STRING\n" +
	"0,1,LINK,3,alpha\n1,2,LINK,4,beta\n2,3,LINK,3,gamma\n"

func writeLinks(t *testing.T) (dir, relFile string) {
	t.Helper()
	dir = t.TempDir()
	relFile = filepath.Join(dir, "LINK.csv")
	if err := os.WriteFile(relFile, []byte(linkCSV), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, relFile
}

func TestLoadKeepsEdgeProperties(t *testing.T) {
	requireUnix(t)
	dir, relFile := writeLinks(t)
	s := start(t, writeFake(t, fakePrimitive))
	ds := &propDataset{dir: dir, relFile: relFile, props: []engine.Column{
		{Name: "ltype", Type: "INT64"}, {Name: "payload", Type: "STRING"},
	}}
	stats, err := s.Load(context.Background(), ds)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stats.Method != "copy (edge properties)" {
		t.Errorf("Method = %q, want copy (edge properties)", stats.Method)
	}
	got, err := os.ReadFile(filepath.Join(s.workDir, "edges.csv"))
	if err != nil {
		t.Fatalf("materialized file: %v", err)
	}
	// The header travels verbatim: it is what names and types the
	// columns, and a copy that lost it would load the edges and none of
	// their values.
	if string(got) != linkCSV {
		t.Errorf("materialized:\n%q\nwant:\n%q", got, linkCSV)
	}
}

func TestLoadFallsBackWhenCopyRefusesTheProperties(t *testing.T) {
	requireUnix(t)
	dir, relFile := writeLinks(t)
	// zu copy refuses edge properties on a file that names a pair twice.
	// The stub refuses every csv, which is the same path.
	refuses := strings.Replace(fakePrimitive, `copy)
  edges="$4"; out="$5"`, `copy)
  edges="$4"; out="$5"
  case "$edges" in
    *.csv) echo "zu copy: invalid argument: edge (1, 2) appears twice" 1>&2; exit 1 ;;
  esac`, 1)
	if refuses == fakePrimitive {
		t.Fatal("the stub's copy arm moved, so this test is not testing the fallback")
	}
	s := start(t, writeFake(t, refuses))
	ds := &propDataset{dir: dir, relFile: relFile, props: []engine.Column{
		{Name: "ltype", Type: "INT64"},
	}}
	stats, err := s.Load(context.Background(), ds)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stats.Method != "copy (edge properties dropped)" {
		t.Errorf("Method = %q, want copy (edge properties dropped)", stats.Method)
	}
	if stats.Edges != 3 {
		t.Errorf("Edges = %d, want 3", stats.Edges)
	}
}

func TestPropRel(t *testing.T) {
	int64Col := []engine.Column{{Name: "ltype", Type: "INT64"}}
	cases := []struct {
		name string
		ds   *propDataset
		want bool
	}{
		{"one rel with properties", &propDataset{props: int64Col}, true},
		{"one rel with no properties", &propDataset{}, false},
		{"two rel tables", &propDataset{props: int64Col, rels: 2}, false},
		{"a type zu copy has no column for", &propDataset{
			props: []engine.Column{{Name: "at", Type: "DATETIME"}},
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			typ, ok := propRel(tc.ds)
			if ok != tc.want {
				t.Fatalf("propRel = %q, %v, want ok %v", typ, ok, tc.want)
			}
			if ok && typ != "LINK" {
				t.Errorf("propRel = %q, want LINK", typ)
			}
		})
	}
}
