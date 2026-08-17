package zu

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// fakeTableCopy is a zu that knows the table form: it records the whole
// argument list where the test can read it, then prints the table form of
// copy's stats output as verified against zu 0.0.1 (2026-08-18).
const fakeTableCopy = `cmd="$1"
case "$cmd" in
--version) echo "zu 9.9.9-test" ;;
help) echo "commands: copy, convert, verify, stat, lookup, neighbors, edge" ;;
copy)
  for a in "$@"; do out="$a"; done
  echo "$@" > "$ZU_ARGS"
  printf 'zu1data' > "$out"
  echo "copied 9 edges, 6 nodes, 2 node table(s), 2 rel table(s)"
  echo "total 0.25s, 1.25 M edges/s end to end, 4096 bytes on disk"
  ;;
*) echo "zu: unknown command '$cmd'" 1>&2; exit 1 ;;
esac
`

// tableDataset is two node labels and two rel types, which is the shape
// the flat load used to collapse into a bare edge list.
type tableDataset struct {
	dir   string
	files map[string][]string
	// noEnd drops the End of one rel type, which is a manifest the
	// table form cannot take.
	noEnd bool
	// orphan adds a node label no rel type touches.
	orphan bool
}

func (d *tableDataset) Name() string               { return "table-ds" }
func (d *tableDataset) Checksum() string           { return "sha256:test" }
func (d *tableDataset) Dir() string                { return d.dir }
func (d *tableDataset) Manifest() *engine.Manifest { return nil }
func (d *tableDataset) Schema() engine.Schema {
	s := engine.Schema{
		Nodes: map[string]engine.NodeSchema{
			"Account": {ID: engine.Column{Name: "id", Type: "ID"}},
			"Person":  {ID: engine.Column{Name: "id", Type: "ID"}},
		},
		Rels: map[string]engine.RelSchema{
			"TRANSFER": {Start: "Account", End: "Account"},
			"OWN":      {Start: "Person", End: "Account"},
		},
	}
	if d.noEnd {
		s.Rels["OWN"] = engine.RelSchema{Start: "Person"}
	}
	if d.orphan {
		s.Nodes["Loan"] = engine.NodeSchema{ID: engine.Column{Name: "id", Type: "ID"}}
	}
	return s
}
func (d *tableDataset) NodeFiles(name string) ([]string, error) { return d.files[name], nil }
func (d *tableDataset) RelFiles(typ string) ([]string, error)   { return d.files[typ], nil }
func (d *tableDataset) Params(string) ([]map[string]engine.Value, error) {
	return nil, nil
}
func (d *tableDataset) Statements() []string { return nil }

// writeTables lays out a two-label, two-type dataset on disk, with the
// Account nodes split across two files and one column zu has no lane for.
func writeTables(t *testing.T) *tableDataset {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	return &tableDataset{dir: dir, files: map[string][]string{
		"Account": {
			write("account-0.csv", "id:ID,balance:FLOAT64,opened:DATETIME\n0,10.5,2020-01-01\n1,20.5,2020-01-02\n"),
			write("account-1.csv", "id:ID,balance:FLOAT64,opened:DATETIME\n2,30.5,2020-01-03\n"),
		},
		"Person": {write("person.csv", "id:ID,name:STRING\n0,ann\n1,bo\n")},
		"TRANSFER": {write("transfer.csv", ":START_ID,:END_ID,:TYPE,amount:FLOAT64,ts:INT64\n"+
			"0,1,TRANSFER,5.5,100\n0,1,TRANSFER,6.5,200\n1,2,TRANSFER,7.5,300\n")},
		"OWN": {write("own.csv", ":START_ID,:END_ID,:TYPE\n0,0,OWN\n1,1,OWN\n")},
	}}
}

// The table form has to reach zu as one --node per label and one --rel
// per type with both of its ends, since that binding is the whole reason
// a finbench read can name a label at all.
func TestCopyTablesArguments(t *testing.T) {
	requireUnix(t)
	ds := writeTables(t)
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("ZU_ARGS", argsFile)

	workDir := t.TempDir()
	dbPath := filepath.Join(workDir, "bench.zu1")
	stats, err := copyTables(context.Background(), "zu", writeFake(t, fakeTableCopy), workDir, dbPath, ds)
	if err != nil {
		t.Fatalf("copyTables: %v", err)
	}

	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("the fake recorded no arguments: %v", err)
	}
	args := strings.Fields(strings.TrimSpace(string(raw)))
	if args[0] != "copy" || args[len(args)-1] != dbPath {
		t.Fatalf("args = %v, want copy ... %s", args, dbPath)
	}
	want := []string{
		"--node", "Account=", "--node", "Person=",
		"--rel", "OWN=Person:Account:", "--rel", "TRANSFER=Account:Account:",
	}
	for i, w := range want {
		got := args[1+i]
		if !strings.HasPrefix(got, w) {
			t.Errorf("arg %d = %q, want it to start with %q", 1+i, got, w)
		}
	}

	if !strings.HasPrefix(stats.Method, "copy (tables") {
		t.Errorf("Method = %q, want the table form", stats.Method)
	}
	if !strings.Contains(stats.Method, "opened") {
		t.Errorf("Method = %q, want it to name the dropped column", stats.Method)
	}
	if stats.Edges != 9 || stats.Nodes != 6 {
		t.Errorf("Edges, Nodes = %d, %d, want 9, 6 from copy's own output", stats.Edges, stats.Nodes)
	}
	if stats.BytesOnDisk != 4096 {
		t.Errorf("BytesOnDisk = %d, want 4096", stats.BytesOnDisk)
	}
	if stats.Duration != 250*time.Millisecond {
		t.Errorf("Duration = %v, want 250ms", stats.Duration)
	}
}

// A table's files are concatenated with one header, and a column zu has
// no lane for is dropped rather than failing the table.
func TestMaterializeTable(t *testing.T) {
	ds := writeTables(t)
	dst := filepath.Join(t.TempDir(), "account.csv")
	dropped, rows, err := materializeTable(ds.files["Account"], dst)
	if err != nil {
		t.Fatalf("materializeTable: %v", err)
	}
	if rows != 3 {
		t.Errorf("rows = %d, want 3", rows)
	}
	if len(dropped) != 1 || dropped[0] != "opened" {
		t.Errorf("dropped = %v, want [opened]", dropped)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	const want = "id:ID,balance:FLOAT64\n0,10.5\n1,20.5\n2,30.5\n"
	if string(got) != want {
		t.Errorf("materialized:\n%q\nwant:\n%q", got, want)
	}
}

// zu splits a line on commas and nothing else, so a value holding one is
// refused where the file and the column are still known, rather than
// loading as two columns that silently shift every later value.
func TestMaterializeTableRefusesACommaInAValue(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "person.csv")
	if err := os.WriteFile(src, []byte("id:ID,name:STRING\n0,\"doe, jane\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := materializeTable([]string{src}, filepath.Join(dir, "out.csv"))
	if err == nil || !strings.Contains(err.Error(), "column break") {
		t.Fatalf("err = %v, want it to name the comma", err)
	}
}

// tableNames is the guard: a manifest that does not describe a graph of
// bound tables is one the table form has to decline, so the caller can
// fall back instead of loading something wrong.
func TestTableNames(t *testing.T) {
	ds := writeTables(t)
	labels, typs, err := tableNames(ds.Schema())
	if err != nil {
		t.Fatalf("tableNames: %v", err)
	}
	if strings.Join(labels, ",") != "Account,Person" {
		t.Errorf("labels = %v, want Account,Person in order", labels)
	}
	if strings.Join(typs, ",") != "OWN,TRANSFER" {
		t.Errorf("types = %v, want OWN,TRANSFER in order", typs)
	}

	cases := []struct {
		name   string
		schema engine.Schema
		want   string
	}{
		{"a rel type missing an end", (&tableDataset{noEnd: true}).Schema(), "both of its ends"},
		{"a label no rel type touches", (&tableDataset{orphan: true}).Schema(), "end of no rel table"},
		{"no tables at all", engine.Schema{}, "at least one node table"},
		{"a rel end that is no label", engine.Schema{
			Nodes: map[string]engine.NodeSchema{"Account": {}},
			Rels:  map[string]engine.RelSchema{"OWN": {Start: "Person", End: "Account"}},
		}, "which is no node table"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := tableNames(tc.schema); err == nil {
				t.Fatal("tableNames accepted a schema the table form cannot take")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

// The table form is tried first and the flat form is the fallback, so a
// dataset the table form declines still loads and still says which form
// it took.
func TestLoadFallsBackToTheFlatForm(t *testing.T) {
	requireUnix(t)
	ds := writeTables(t)
	ds.noEnd = true
	t.Setenv("ZU_ARGS", filepath.Join(t.TempDir(), "args"))

	workDir := t.TempDir()
	dbPath := filepath.Join(workDir, "bench.zu1")
	stats, err := copyDataset(context.Background(), "zu", writeFake(t, fakeCopy), workDir, dbPath, ds)
	if err != nil {
		t.Fatalf("copyDataset: %v", err)
	}
	if strings.HasPrefix(stats.Method, "copy (tables") {
		t.Errorf("Method = %q, want a form other than the table one", stats.Method)
	}
}
