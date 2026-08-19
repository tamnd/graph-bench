//go:build sqlite

package sqlite

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/graph-bench/engine"
)

// A triangle with a tail: 1->2, 2->3, 3->1, 3->4. Small enough that every
// expected answer below can be read off it by hand.
var (
	testNodes = []int{1, 2, 3, 4}
	testEdges = [][2]int{{1, 2}, {2, 3}, {3, 1}, {3, 4}}
)

func TestLoadAndQuery(t *testing.T) {
	ctx := context.Background()
	sess := start(t, WAL)
	stats, err := sess.Load(ctx, writeDataset(t))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if stats.Nodes != 4 || stats.Edges != 4 {
		t.Errorf("load counted %d nodes and %d edges, want 4 and 4", stats.Nodes, stats.Edges)
	}
	if stats.BytesOnDisk <= 0 {
		t.Errorf("load reported %d bytes on disk, want a real size", stats.BytesOnDisk)
	}
	if stats.Method != "insert-tx" {
		t.Errorf("load method %q, want insert-tx", stats.Method)
	}

	for _, tc := range []struct {
		name   string
		text   string
		params map[string]engine.Value
		cols   []string
		rows   [][]engine.Value
	}{
		{
			name: "point read",
			text: `SELECT id FROM node WHERE id = CAST($id AS BIGINT)`,
			// The pools carry ids as strings, and the id columns are
			// BIGINT, so this is also the check that the binding converts.
			params: map[string]engine.Value{"id": "3"},
			cols:   []string{"id"},
			rows:   [][]engine.Value{{int64(3)}},
		},
		{
			name:   "point miss is no rows, not a null row",
			text:   `SELECT id FROM node WHERE id = CAST($id AS BIGINT)`,
			params: map[string]engine.Value{"id": "99"},
			cols:   []string{"id"},
		},
		{
			name:   "edge probe decodes as a boolean",
			text:   `SELECT count(*) > 0 AS "found::bool" FROM edge WHERE src = CAST($src AS BIGINT) AND dst = CAST($dst AS BIGINT)`,
			params: map[string]engine.Value{"src": "1", "dst": "2"},
			cols:   []string{"found"},
			rows:   [][]engine.Value{{true}},
		},
		{
			name:   "absent edge is false and not zero",
			text:   `SELECT count(*) > 0 AS "found::bool" FROM edge WHERE src = CAST($src AS BIGINT) AND dst = CAST($dst AS BIGINT)`,
			params: map[string]engine.Value{"src": "2", "dst": "1"},
			cols:   []string{"found"},
			rows:   [][]engine.Value{{false}},
		},
		{
			name:   "two hops out of 1 reaches 3",
			text:   `SELECT count(DISTINCT e2.dst) AS n FROM edge e1 JOIN edge e2 ON e2.src = e1.dst WHERE e1.src = CAST($seed AS BIGINT)`,
			params: map[string]engine.Value{"seed": "1"},
			cols:   []string{"n"},
			rows:   [][]engine.Value{{int64(1)}},
		},
		{
			name: "shortest path counts hops",
			text: `WITH RECURSIVE reach(v, d) AS (
  SELECT CAST($src AS BIGINT), 0
  UNION
  SELECT e.dst, r.d + 1 FROM reach r JOIN edge e ON e.src = r.v WHERE r.d < 64
)
SELECT min(d) AS d FROM reach WHERE v = CAST($dst AS BIGINT) HAVING count(*) > 0`,
			params: map[string]engine.Value{"src": "1", "dst": "4"},
			cols:   []string{"d"},
			rows:   [][]engine.Value{{int64(3)}},
		},
		{
			// The one that would be a NULL row without the HAVING, which
			// verification reads as an answer of unknown rather than as no
			// path.
			name: "no path is no rows",
			text: `WITH RECURSIVE reach(v, d) AS (
  SELECT CAST($src AS BIGINT), 0
  UNION
  SELECT e.dst, r.d + 1 FROM reach r JOIN edge e ON e.src = r.v WHERE r.d < 64
)
SELECT min(d) AS d FROM reach WHERE v = CAST($dst AS BIGINT) HAVING count(*) > 0`,
			params: map[string]engine.Value{"src": "4", "dst": "1"},
			cols:   []string{"d"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cols, rows := query(t, sess, tc.text, tc.params)
			if strings.Join(cols, ",") != strings.Join(tc.cols, ",") {
				t.Fatalf("columns %v, want %v", cols, tc.cols)
			}
			if len(rows) != len(tc.rows) {
				t.Fatalf("%d rows %v, want %d %v", len(rows), rows, len(tc.rows), tc.rows)
			}
			for i := range rows {
				for j := range rows[i] {
					if rows[i][j] != tc.rows[i][j] {
						t.Errorf("row %d col %d = %v (%T), want %v (%T)",
							i, j, rows[i][j], rows[i][j], tc.rows[i][j], tc.rows[i][j])
					}
				}
			}
		})
	}
}

// The memory mode has no file to size, so its footprint is the database's
// own page accounting. It still has to be a real number: a zero there
// would read in a report as an engine that stores nothing.
func TestMemoryModeStillReportsAFootprint(t *testing.T) {
	sess := start(t, Memory)
	stats, err := sess.Load(context.Background(), writeDataset(t))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if stats.BytesOnDisk <= 0 {
		t.Errorf("memory mode reported %d bytes, want its page footprint", stats.BytesOnDisk)
	}
}

func TestVersionIsLive(t *testing.T) {
	v, err := start(t, WAL).Version(context.Background())
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.HasPrefix(v, "3.") {
		t.Errorf("version %q does not look like a SQLite version", v)
	}
}

// A dataset with two node labels does not fit one node table, and the
// load says so rather than merging them.
func TestLoadRefusesWhatTheSchemaCannotHold(t *testing.T) {
	ds := writeDataset(t)
	ds.schema.Nodes["Other"] = ds.schema.Nodes["Node"]
	_, err := start(t, WAL).Load(context.Background(), ds)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "2 labels") {
		t.Errorf("error %q does not say what did not fit", err)
	}
}

func start(t *testing.T, mode Mode) engine.Session {
	t.Helper()
	e := &Engine{mode: mode}
	sess, err := e.Start(context.Background(), engine.Config{})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { sess.Close(context.Background()) })
	return sess
}

func query(t *testing.T, sess engine.Session, text string, params map[string]engine.Value) ([]string, [][]engine.Value) {
	t.Helper()
	res, err := sess.Exec(context.Background(), engine.Op{
		QueryID: "test",
		Dialect: engine.SQL,
		Text:    text,
		Params:  params,
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	defer res.Close()
	cols := res.Columns()
	var rows [][]engine.Value
	for res.Next() {
		row := make([]engine.Value, len(res.Row()))
		copy(row, res.Row())
		rows = append(rows, row)
	}
	if err := res.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return cols, rows
}

// writeDataset writes the little graph out in the canonical CSV layout,
// header suffixes and all, so the load path under test is the one the
// harness uses and not a shortcut.
func writeDataset(t *testing.T) *testDataset {
	t.Helper()
	dir := t.TempDir()
	nodes := filepath.Join(dir, "Node.csv")
	var b strings.Builder
	b.WriteString("id:ID,:LABEL\n")
	for _, id := range testNodes {
		fmt.Fprintf(&b, "%d,Node\n", id)
	}
	if err := os.WriteFile(nodes, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	rels := filepath.Join(dir, "EDGE.csv")
	b.Reset()
	b.WriteString(":START_ID,:END_ID,:TYPE\n")
	for _, e := range testEdges {
		fmt.Fprintf(&b, "%d,%d,EDGE\n", e[0], e[1])
	}
	if err := os.WriteFile(rels, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return &testDataset{
		dir:   dir,
		files: map[string][]string{"Node": {nodes}, "EDGE": {rels}},
		schema: engine.Schema{
			Nodes: map[string]engine.NodeSchema{"Node": {ID: engine.Column{Name: "id", Type: "ID"}}},
			Rels:  map[string]engine.RelSchema{"EDGE": {Start: "Node", End: "Node"}},
		},
	}
}

type testDataset struct {
	dir    string
	files  map[string][]string
	schema engine.Schema
}

var _ engine.Dataset = (*testDataset)(nil)

func (d *testDataset) Name() string                             { return "test" }
func (d *testDataset) Checksum() string                         { return "sha256:test" }
func (d *testDataset) Dir() string                              { return d.dir }
func (d *testDataset) Manifest() *engine.Manifest               { return nil }
func (d *testDataset) Schema() engine.Schema                    { return d.schema }
func (d *testDataset) NodeFiles(label string) ([]string, error) { return d.files[label], nil }
func (d *testDataset) RelFiles(typ string) ([]string, error)    { return d.files[typ], nil }
func (d *testDataset) Statements() []string                     { return nil }
func (d *testDataset) Params(string) ([]map[string]engine.Value, error) {
	return nil, nil
}
