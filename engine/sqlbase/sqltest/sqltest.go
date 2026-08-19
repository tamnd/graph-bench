// Package sqltest is the conformance suite every relational adapter runs.
//
// The adapters share sqlbase and share the sql dialect, so what is worth
// testing about them is shared too: that the load puts the right number
// of rows in the right two tables, that a $name parameter reaches the
// driver as a bound integer, that an existence probe comes back as a
// boolean and not as a 1, that a query with no answer returns no rows
// rather than one NULL row, and that a dataset the two-table schema
// cannot hold is refused instead of quietly flattened.
//
// An adapter's own test file is then a call to Run plus whatever is
// genuinely specific to that engine. Anything that has to hold for
// SQLite, DuckDB and PostgreSQL alike belongs here, so a new adapter
// inherits it rather than reimplementing it slightly differently.
//
// The dataset is a real one, written through dataset.Writer in the
// canonical layout and opened back through dataset.Open, so the load path
// under test is the one the harness uses and not a shortcut around it.
package sqltest

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/engine"
)

// The graph: a triangle with a tail, 1->2, 2->3, 3->1 and 3->4. It is
// small enough that every expected answer below can be read off it by
// hand, and directed enough that a query which quietly treats the edges
// as undirected gets a different answer.
var edges = [][2]int64{{1, 2}, {2, 3}, {3, 1}, {3, 4}}

const nodeCount = 4

// Run is the suite. It starts one session, loads the graph into it and
// runs the query table; the session is closed when the test ends.
func Run(t *testing.T, e engine.Engine) {
	t.Helper()
	ctx := context.Background()
	sess := Start(t, e)
	stats, err := sess.Load(ctx, Dataset(t))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if stats.Nodes != nodeCount || stats.Edges != int64(len(edges)) {
		t.Errorf("load counted %d nodes and %d edges, want %d and %d",
			stats.Nodes, stats.Edges, nodeCount, len(edges))
	}
	if stats.Duration <= 0 {
		t.Errorf("load took %v, want a measured duration", stats.Duration)
	}
	if stats.Method == "" {
		t.Error("load reported no method, and a report column names it")
	}
	// Either a real footprint or an explicit -1, which is what LoadStats
	// means by not meaningful. Zero is the one wrong answer: it reads in
	// a report as an engine that stores nothing.
	if stats.BytesOnDisk == 0 || stats.BytesOnDisk < -1 {
		t.Errorf("load reported %d bytes stored, want a footprint or -1", stats.BytesOnDisk)
	}

	if v, err := sess.Version(ctx); err != nil {
		t.Errorf("version: %v", err)
	} else if v == "" {
		t.Error("version is empty, and results are stamped with it")
	}

	for _, tc := range queries {
		t.Run(tc.name, func(t *testing.T) {
			cols, rows := query(t, sess, tc.text, tc.params)
			if strings.Join(cols, ",") != strings.Join(tc.cols, ",") {
				t.Fatalf("columns %v, want %v", cols, tc.cols)
			}
			if len(rows) != len(tc.rows) {
				t.Fatalf("got %d rows %v, want %d %v", len(rows), rows, len(tc.rows), tc.rows)
			}
			for i := range rows {
				if len(rows[i]) != len(tc.rows[i]) {
					t.Fatalf("row %d has %d values %v, want %d", i, len(rows[i]), rows[i], len(tc.rows[i]))
				}
				for j := range rows[i] {
					if rows[i][j] != tc.rows[i][j] {
						t.Errorf("row %d col %d = %v (%T), want %v (%T)",
							i, j, rows[i][j], rows[i][j], tc.rows[i][j], tc.rows[i][j])
					}
				}
			}
		})
	}

	t.Run("a dataset the schema cannot hold is refused", func(t *testing.T) {
		_, err := Start(t, e).Load(ctx, twoLabelDataset(t))
		if err == nil {
			t.Fatal("want an error")
		}
		if !strings.Contains(err.Error(), "2 labels") {
			t.Errorf("error %q does not say what did not fit", err)
		}
	})
}

// queries are the shapes the micro workload's sql texts are made of, cut
// down to this graph. They are the same texts, not paraphrases: if one of
// these stops working on an engine, that engine's micro numbers are gone
// too.
var queries = []struct {
	name   string
	text   string
	params map[string]engine.Value
	cols   []string
	rows   [][]engine.Value
}{
	{
		name: "point read",
		text: `SELECT id FROM node WHERE id = CAST($id AS BIGINT)`,
		// The params pools carry ids as strings and the id columns are
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
		// The edge 2->1 does not exist and 1->2 does, so an adapter that
		// lost the direction somewhere answers true here.
		name:   "an absent edge is false and not zero",
		text:   `SELECT count(*) > 0 AS "found::bool" FROM edge WHERE src = CAST($src AS BIGINT) AND dst = CAST($dst AS BIGINT)`,
		params: map[string]engine.Value{"src": "2", "dst": "1"},
		cols:   []string{"found"},
		rows:   [][]engine.Value{{false}},
	},
	{
		name:   "one hop",
		text:   `SELECT count(*) AS n FROM edge WHERE src = CAST($seed AS BIGINT)`,
		params: map[string]engine.Value{"seed": "3"},
		cols:   []string{"n"},
		rows:   [][]engine.Value{{int64(2)}},
	},
	{
		name:   "two hops out of 1 reach only 3",
		text:   `SELECT count(DISTINCT e2.dst) AS n FROM edge e1 JOIN edge e2 ON e2.src = e1.dst WHERE e1.src = CAST($seed AS BIGINT)`,
		params: map[string]engine.Value{"seed": "1"},
		cols:   []string{"n"},
		rows:   [][]engine.Value{{int64(1)}},
	},
	{
		name: "a recursive walk stops at its depth bound",
		text: `WITH RECURSIVE reach(v, d) AS (
  SELECT dst, 1 FROM edge WHERE src = CAST($seed AS BIGINT)
  UNION
  SELECT e.dst, r.d + 1 FROM reach r JOIN edge e ON e.src = r.v WHERE r.d < 3
)
SELECT count(DISTINCT v) AS n FROM reach`,
		// Out of 1 in at most three hops: 2, 3, 1, 4.
		params: map[string]engine.Value{"seed": "1"},
		cols:   []string{"n"},
		rows:   [][]engine.Value{{int64(4)}},
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
		// The one that would be a single NULL row without the HAVING,
		// which verification reads as an answer of unknown rather than as
		// no path. Node 4 is the tail and reaches nothing.
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
	{
		name: "scan and aggregate",
		text: `SELECT count(*) AS n, avg(CAST(id AS DOUBLE PRECISION)) AS "avgId" FROM node`,
		cols: []string{"n", "avgId"},
		rows: [][]engine.Value{{int64(4), 2.5}},
	},
	{
		name: "directed triangles",
		text: `SELECT count(*) AS n FROM edge a
  JOIN edge b ON b.src = a.dst
  JOIN edge c ON c.src = b.dst AND c.dst = a.src`,
		cols: []string{"n"},
		// One triangle, counted once per starting edge.
		rows: [][]engine.Value{{int64(3)}},
	},
}

// Start opens a session on a fresh database and closes it when the test
// ends. It is exported because an adapter's own tests need one too.
func Start(t *testing.T, e engine.Engine) engine.Session {
	t.Helper()
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
		QueryID: "sqltest",
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

// Dataset writes the graph out in the canonical layout and opens it back.
func Dataset(t *testing.T) engine.Dataset {
	t.Helper()
	dir := t.TempDir()
	w := writer(t, dir)
	writeNodes(t, w, "Node", 1, nodeCount)
	rw, err := w.RelFile("EDGE", "", "", relHeader)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range edges {
		if err := rw.Write([]string{itoa(e[0]), itoa(e[1]), "EDGE"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := rw.Close(); err != nil {
		t.Fatal(err)
	}
	return finalize(t, w, dir)
}

// twoLabelDataset is the same graph with its nodes split across two
// labels, which is a shape the two-table schema has no place for.
func twoLabelDataset(t *testing.T) engine.Dataset {
	t.Helper()
	dir := t.TempDir()
	w := writer(t, dir)
	writeNodes(t, w, "Node", 1, 2)
	writeNodes(t, w, "Other", 3, nodeCount)
	rw, err := w.RelFile("EDGE", "Node", "Other", relHeader)
	if err != nil {
		t.Fatal(err)
	}
	if err := rw.Write([]string{"1", "3", "EDGE"}); err != nil {
		t.Fatal(err)
	}
	if err := rw.Close(); err != nil {
		t.Fatal(err)
	}
	return finalize(t, w, dir)
}

var (
	nodeHeader = []engine.Column{{Name: "id", Type: "ID"}, {Type: "LABEL"}}
	relHeader  = []engine.Column{{Type: "START_ID"}, {Type: "END_ID"}, {Type: "TYPE"}}
)

func writer(t *testing.T, dir string) *dataset.Writer {
	t.Helper()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func writeNodes(t *testing.T, w *dataset.Writer, label string, from, to int64) {
	t.Helper()
	rw, err := w.NodeFile(label, nodeHeader)
	if err != nil {
		t.Fatal(err)
	}
	for id := from; id <= to; id++ {
		if err := rw.Write([]string{itoa(id), label}); err != nil {
			t.Fatal(err)
		}
	}
	if err := rw.Close(); err != nil {
		t.Fatal(err)
	}
}

func finalize(t *testing.T, w *dataset.Writer, dir string) engine.Dataset {
	t.Helper()
	if _, err := w.Finalize(&engine.Manifest{Name: "sqltest", Kind: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
