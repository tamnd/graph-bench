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

// The loader is the one part of this adapter that still runs the zu CLI,
// so it is tested against a fake zu binary: a /bin/sh script emulating
// the copy verb as verified against zu 0.0.1 (2026-08-10). Everything
// else in the adapter is a C call and is covered by inproc_test.go, which
// needs a real libzu and a real binary.

// fakeCopy answers --version, help and copy, and refuses anything else.
const fakeCopy = `cmd="$1"
case "$cmd" in
--version) echo "zu 9.9.9-test" ;;
help) echo "commands: copy, convert, verify, stat, lookup, neighbors, edge" ;;
copy)
  for a in "$@"; do edges="$out"; out="$a"; done
  n=$(wc -l < "$edges" | tr -d ' ')
  printf 'zu1data' > "$out"
  echo "copied $n edges, 7 nodes, 2 groups"
  echo "parse 0.10s, encode+write 0.15s, total 0.25s"
  echo "1.25 M edges/s end to end, 4096 bytes on disk, 12.50 bits/edge fwd, 10.00 bits/edge bwd"
  ;;
*) echo "zu: unknown command '$cmd'" 1>&2; exit 1 ;;
esac
`

func requireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake zu binary is a /bin/sh script")
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

// loadWith runs the bulk-load path against a fake binary in a fresh work
// dir, and returns the stats plus the work dir so a test can read what
// was materialized.
func loadWith(t *testing.T, bin string, ds engine.Dataset) (engine.LoadStats, string) {
	t.Helper()
	workDir := t.TempDir()
	dbPath := filepath.Join(workDir, "bench.zu1")
	stats, err := copyDataset(context.Background(), "zu", bin, workDir, dbPath, ds)
	if err != nil {
		t.Fatalf("copyDataset: %v", err)
	}
	return stats, workDir
}

// fileDataset is one rel table with no properties, the shape that takes
// the flat 2-column path.
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

// TestDiscoveryFailure checks the binary discovery order reports every
// place it looked, since a load that cannot find zu is a setup problem
// and the error is the whole diagnosis.
func TestDiscoveryFailure(t *testing.T) {
	t.Setenv("ZU_BIN", "")
	t.Setenv("PATH", t.TempDir()) // empty dir: no zu on PATH
	_, err := discoverBinary(engine.Config{
		Values: map[string]string{"bin": filepath.Join(t.TempDir(), "no-such-zu")},
	})
	if err == nil {
		t.Fatal("discovery succeeded with no binary anywhere")
	}
	for _, want := range []string{`config "bin"`, "ZU_BIN", "PATH", "target/release/zu", "target/debug/zu"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("discovery error missing %q:\n%v", want, err)
		}
	}
}

// TestLoadStatsParsing reads copy's own stats output rather than timing
// the process, so a load reports the engine's numbers.
func TestLoadStatsParsing(t *testing.T) {
	requireUnix(t)
	dir := t.TempDir()
	relFile := filepath.Join(dir, "EDGE.csv")
	csv := ":START_ID,:END_ID,:TYPE\n0,1,EDGE\n1,2,EDGE\n2,3,EDGE\n0,2,EDGE\n"
	if err := os.WriteFile(relFile, []byte(csv), 0o644); err != nil {
		t.Fatal(err)
	}

	stats, workDir := loadWith(t, writeFake(t, fakeCopy), &fileDataset{dir: dir, relFile: relFile})
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
	if _, err := os.Stat(filepath.Join(workDir, "bench.zu1")); err != nil {
		t.Errorf("db file not produced: %v", err)
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

// TestLoadKeepsEdgeProperties checks the typed header travels verbatim:
// it is what names and types the columns, and a copy that lost it would
// load the edges and none of their values.
func TestLoadKeepsEdgeProperties(t *testing.T) {
	requireUnix(t)
	dir, relFile := writeLinks(t)
	ds := &propDataset{dir: dir, relFile: relFile, props: []engine.Column{
		{Name: "ltype", Type: "INT64"}, {Name: "payload", Type: "STRING"},
	}}
	stats, workDir := loadWith(t, writeFake(t, fakeCopy), ds)
	if stats.Method != "copy (edge properties)" {
		t.Errorf("Method = %q, want copy (edge properties)", stats.Method)
	}
	got, err := os.ReadFile(filepath.Join(workDir, "edges.csv"))
	if err != nil {
		t.Fatalf("materialized file: %v", err)
	}
	if string(got) != linkCSV {
		t.Errorf("materialized:\n%q\nwant:\n%q", got, linkCSV)
	}
}

// TestLoadFallsBackWhenCopyRefusesTheProperties keeps a load whose copy
// refuses the typed CSV: it retries flat and says so in Method rather
// than failing the run. The stub refuses a pair named twice, which is
// what zu used to refuse and no longer does, so this is now about the
// fallback rather than about that one error.
func TestLoadFallsBackWhenCopyRefusesTheProperties(t *testing.T) {
	requireUnix(t)
	dir, relFile := writeLinks(t)
	refuses := strings.Replace(fakeCopy, `copy)
  for a in "$@"; do edges="$out"; out="$a"; done`, `copy)
  for a in "$@"; do edges="$out"; out="$a"; done
  case "$edges" in
    *.csv) echo "zu copy: invalid argument: edge (1, 2) appears twice" 1>&2; exit 1 ;;
  esac`, 1)
	if refuses == fakeCopy {
		t.Fatal("the stub's copy arm moved, so this test is not testing the fallback")
	}
	ds := &propDataset{dir: dir, relFile: relFile, props: []engine.Column{
		{Name: "ltype", Type: "INT64"},
	}}
	stats, _ := loadWith(t, writeFake(t, refuses), ds)
	if stats.Method != "copy (edge properties dropped)" {
		t.Errorf("Method = %q, want copy (edge properties dropped)", stats.Method)
	}
	if stats.Edges != 3 {
		t.Errorf("Edges = %d, want 3", stats.Edges)
	}
}

// TestPropRel covers the four shapes that decide whether edge properties
// can travel with the edges at all.
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

// TestInfo pins the descriptor: the name every result file is keyed by,
// the plane, and the one-dialect chain that makes a missing zuQL text a
// SKIP instead of a FAIL.
func TestInfo(t *testing.T) {
	info := New().Info()
	if info.Name != "zu" {
		t.Errorf("Name = %q, want zu", info.Name)
	}
	if info.Plane != engine.InProc {
		t.Errorf("Plane = %v, want inproc", info.Plane)
	}
	if len(info.Dialects) != 1 || info.Dialects[0] != engine.ZuQL {
		t.Errorf("Dialects = %v, want [zuql]", info.Dialects)
	}
	if info.Caps.MaxConcurrency != 0 {
		t.Errorf("MaxConcurrency = %d, want 0 (a Session pools connections, so the runner sets the limit)", info.Caps.MaxConcurrency)
	}
}

// nodeDataset is a dataset with one node table, which is what the node
// flag is for. The rel table is there so the schema is a graph.
type nodeDataset struct {
	nodeFiles []string
	relFiles  []string
}

func (d *nodeDataset) Name() string               { return "node-ds" }
func (d *nodeDataset) Checksum() string           { return "sha256:test" }
func (d *nodeDataset) Dir() string                { return "." }
func (d *nodeDataset) Manifest() *engine.Manifest { return nil }
func (d *nodeDataset) Schema() engine.Schema {
	return engine.Schema{
		Nodes: map[string]engine.NodeSchema{"Node": {ID: engine.Column{Name: "id", Type: "ID"}}},
		Rels: map[string]engine.RelSchema{
			"EDGE": {Files: []string{"rels/EDGE.csv"}, Start: "Node", End: "Node"},
		},
	}
}
func (d *nodeDataset) NodeFiles(string) ([]string, error)               { return d.nodeFiles, nil }
func (d *nodeDataset) RelFiles(string) ([]string, error)                { return d.relFiles, nil }
func (d *nodeDataset) Params(string) ([]map[string]engine.Value, error) { return nil, nil }
func (d *nodeDataset) Statements() []string                             { return nil }

// The ids a node table declares have to reach zu copy, because the ones
// no edge names are exactly the ones the edge list cannot report.
func TestNodesFlag(t *testing.T) {
	dir := t.TempDir()
	nodes := filepath.Join(dir, "Node.csv")
	if err := os.WriteFile(nodes, []byte("id:ID,:LABEL\n0,Node\n1,Node\n7,Node\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	flag, err := nodesFlag(&nodeDataset{nodeFiles: []string{nodes}}, work)
	if err != nil {
		t.Fatalf("nodesFlag: %v", err)
	}
	if len(flag) != 2 || flag[0] != "--nodes" {
		t.Fatalf("flag = %v, want --nodes <file>", flag)
	}
	got, err := os.ReadFile(flag[1])
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "0\n1\n7\n" {
		t.Errorf("node list = %q, want the three ids without the header", got)
	}
}

// A dataset whose ids are not numbers loads the way it always did: the
// endpoints are all zu copy can key on, so there is no file to pass and
// no failure either.
func TestNodesFlagSkipsWhatItCannotDeclare(t *testing.T) {
	dir := t.TempDir()
	strIDs := filepath.Join(dir, "Node.csv")
	if err := os.WriteFile(strIDs, []byte("id:ID,:LABEL\nalice,Node\nbob,Node\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	flag, err := nodesFlag(&nodeDataset{nodeFiles: []string{strIDs}}, t.TempDir())
	if err != nil {
		t.Fatalf("nodesFlag: %v", err)
	}
	if flag != nil {
		t.Errorf("flag = %v, want none for string ids", flag)
	}

	flag, err = nodesFlag(&nodeDataset{}, t.TempDir())
	if err != nil {
		t.Fatalf("nodesFlag: %v", err)
	}
	if flag != nil {
		t.Errorf("flag = %v, want none when the dataset lists no node files", flag)
	}
}

// fakeOldCopy is a zu from before the table flags: it refuses --node,
// --rel and --nodes the way a hand-rolled argument parser refuses
// anything it does not know, so a load against it lands on the flat form.
const fakeOldCopy = `cmd="$1"
case "$cmd" in
--version) echo "zu 9.9.9-test" ;;
help) echo "commands: copy, convert, verify, stat, lookup, neighbors, edge" ;;
copy)
  for a in "$@"; do
    case "$a" in
      --node|--nodes|--rel)
        echo "usage: zu copy [--reorder degree|bfs|none] <edges> <out.zu1>" 1>&2
        exit 2
        ;;
    esac
  done
  for a in "$@"; do edges="$out"; out="$a"; done
  printf 'zu1data' > "$out"
  n=$(wc -l < "$edges" | tr -d ' ')
  echo "copied $n edges, 7 nodes, 2 groups"
  ;;
*) echo "zu: unknown command '$cmd'" 1>&2; exit 1 ;;
esac
`

// The node file is an improvement, not a requirement, so a zu that does
// not know the flag still loads. The alternative is a benchmark that
// stops working against every build older than the flag.
func TestLoadRetriesWithoutTheNodeFile(t *testing.T) {
	requireUnix(t)
	dir := t.TempDir()
	nodes := filepath.Join(dir, "Node.csv")
	if err := os.WriteFile(nodes, []byte("id:ID\n0\n1\n7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rels := filepath.Join(dir, "EDGE.csv")
	if err := os.WriteFile(rels, []byte(":START_ID,:END_ID,:TYPE\n0,1,EDGE\n1,7,EDGE\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stats, _ := loadWith(t, writeFake(t, fakeOldCopy), &nodeDataset{
		nodeFiles: []string{nodes},
		relFiles:  []string{rels},
	})
	if stats.Method != "copy" {
		t.Errorf("Method = %q, want copy", stats.Method)
	}
	if stats.Edges != 2 {
		t.Errorf("Edges = %d, want 2", stats.Edges)
	}
}
