//go:build ladybug

package ladybug

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// fixtureDS is a minimal file-backed engine.Dataset written by hand so the
// tests do not depend on the dataset package.
type fixtureDS struct {
	dir    string
	schema engine.Schema
}

func (f *fixtureDS) Name() string               { return "ladybug-fixture" }
func (f *fixtureDS) Checksum() string           { return "" }
func (f *fixtureDS) Dir() string                { return f.dir }
func (f *fixtureDS) Manifest() *engine.Manifest { return nil }
func (f *fixtureDS) Schema() engine.Schema      { return f.schema }
func (f *fixtureDS) NodeFiles(label string) ([]string, error) {
	ns, ok := f.schema.Nodes[label]
	if !ok {
		return nil, fmt.Errorf("no node label %q", label)
	}
	return f.abs(ns.Files), nil
}
func (f *fixtureDS) RelFiles(typ string) ([]string, error) {
	rs, ok := f.schema.Rels[typ]
	if !ok {
		return nil, fmt.Errorf("no rel type %q", typ)
	}
	return f.abs(rs.Files), nil
}
func (f *fixtureDS) Params(string) ([]map[string]engine.Value, error) { return nil, nil }
func (f *fixtureDS) Statements() []string                             { return nil }

func (f *fixtureDS) abs(rel []string) []string {
	out := make([]string, len(rel))
	for i, r := range rel {
		out[i] = filepath.Join(f.dir, r)
	}
	return out
}

// newFixture writes a tiny Person/KNOWS graph in canonical layout:
// 3 people, 3 KNOWS edges forming a cycle.
func newFixture(t *testing.T) *fixtureDS {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "nodes", "Person.csv"),
		"id:ID,name:STRING,age:INT64,:LABEL\n"+
			"1,alice,30,Person\n"+
			"2,bob,25,Person\n"+
			"3,carol,41,Person\n")
	mustWrite(t, filepath.Join(dir, "rels", "KNOWS.csv"),
		":START_ID,:END_ID,since:INT64,:TYPE\n"+
			"1,2,2010,KNOWS\n"+
			"2,3,2015,KNOWS\n"+
			"3,1,2020,KNOWS\n")
	return &fixtureDS{
		dir: dir,
		schema: engine.Schema{
			Nodes: map[string]engine.NodeSchema{
				"Person": {
					Files: []string{"nodes/Person.csv"},
					ID:    engine.Column{Name: "id", Type: "ID"},
					Properties: []engine.Column{
						{Name: "name", Type: "STRING"},
						{Name: "age", Type: "INT64"},
					},
					Labels: []string{"Person"},
				},
			},
			Rels: map[string]engine.RelSchema{
				"KNOWS": {
					Files: []string{"rels/KNOWS.csv"},
					Start: "Person",
					End:   "Person",
					Properties: []engine.Column{
						{Name: "since", Type: "INT64"},
					},
				},
			},
		},
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// startSession opens a fresh database under t.TempDir.
func startSession(t *testing.T, e *Engine) engine.Session {
	t.Helper()
	ctx := context.Background()
	cfg := engine.Config{Values: map[string]string{"path": filepath.Join(t.TempDir(), "db")}}
	s, err := e.Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(ctx) })
	return s
}

func execOne(t *testing.T, s engine.Session, text string, params map[string]engine.Value) []engine.Value {
	t.Helper()
	res, err := s.Exec(context.Background(), engine.Op{Text: text, Params: params})
	if err != nil {
		t.Fatalf("Exec %q: %v", text, err)
	}
	defer res.Close()
	if !res.Next() {
		t.Fatalf("Exec %q: no rows (err=%v)", text, res.Err())
	}
	row := append([]engine.Value(nil), res.Row()...)
	if err := res.Err(); err != nil {
		t.Fatalf("Exec %q: result err: %v", text, err)
	}
	return row
}

// TestStartLoadExec is the round trip: open, COPY-load the fixture, count.
func TestStartLoadExec(t *testing.T) {
	e := New()
	s := startSession(t, e)
	ctx := context.Background()

	v, err := s.Version(ctx)
	if err != nil || v == "" {
		t.Fatalf("Version = %q, %v", v, err)
	}
	t.Logf("ladybug version: %s", v)

	stats, err := s.Load(ctx, newFixture(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stats.Method != "copy" {
		t.Errorf("Method = %q, want copy", stats.Method)
	}
	if stats.BytesOnDisk <= 0 {
		t.Errorf("BytesOnDisk = %d, want > 0", stats.BytesOnDisk)
	}

	row := execOne(t, s, "MATCH (:Person)-[:KNOWS]->(:Person) RETURN count(*) AS n", nil)
	if n, ok := row[0].(int64); !ok || n != 3 {
		t.Errorf("edge count = %v (%T), want int64 3", row[0], row[0])
	}

	// Rel property survived the stripped-CSV rewrite.
	row = execOne(t, s, "MATCH (a:Person {id: 1})-[k:KNOWS]->(b) RETURN k.since, b.name", nil)
	if since, ok := row[0].(int64); !ok || since != 2010 {
		t.Errorf("since = %v (%T), want int64 2010", row[0], row[0])
	}
	if name, ok := row[1].(string); !ok || name != "bob" {
		t.Errorf("name = %v (%T), want bob", row[1], row[1])
	}
}

// TestPreparedReuse runs the same text with different params, exercising
// the prepared-statement cache.
func TestPreparedReuse(t *testing.T) {
	e := New()
	s := startSession(t, e)
	if _, err := s.Load(context.Background(), newFixture(t)); err != nil {
		t.Fatalf("Load: %v", err)
	}

	const q = "MATCH (p:Person) WHERE p.id = $id RETURN p.name"
	want := map[int64]string{1: "alice", 2: "bob", 3: "carol"}
	for id, name := range want {
		row := execOne(t, s, q, map[string]engine.Value{"id": id})
		if got, ok := row[0].(string); !ok || got != name {
			t.Errorf("id %d: name = %v (%T), want %q", id, row[0], row[0], name)
		}
	}

	sess := s.(*session)
	sess.mu.Lock()
	cached := len(sess.prep)
	sess.mu.Unlock()
	if cached != 1 {
		t.Errorf("prepared cache size = %d, want 1", cached)
	}
}

// TestValueDecode covers the scalar, temporal, list, node, and rel decode
// paths of extractValue.
func TestValueDecode(t *testing.T) {
	e := New()
	s := startSession(t, e)
	if _, err := s.Load(context.Background(), newFixture(t)); err != nil {
		t.Fatalf("Load: %v", err)
	}

	row := execOne(t, s,
		`RETURN 42 AS i, 1.5 AS f, 'hi' AS s, true AS b, NULL AS z,
		        [1,2,3] AS xs, date('2020-01-31') AS d,
		        timestamp('2020-01-31 12:30:00') AS ts`, nil)

	if v, ok := row[0].(int64); !ok || v != 42 {
		t.Errorf("int = %v (%T), want int64 42", row[0], row[0])
	}
	if v, ok := row[1].(float64); !ok || v != 1.5 {
		t.Errorf("float = %v (%T), want 1.5", row[1], row[1])
	}
	if v, ok := row[2].(string); !ok || v != "hi" {
		t.Errorf("string = %v (%T), want hi", row[2], row[2])
	}
	if v, ok := row[3].(bool); !ok || v != true {
		t.Errorf("bool = %v (%T), want true", row[3], row[3])
	}
	if row[4] != nil {
		t.Errorf("null = %v (%T), want nil", row[4], row[4])
	}
	if xs, ok := row[5].([]engine.Value); !ok || len(xs) != 3 || xs[0] != int64(1) {
		t.Errorf("list = %v (%T), want [1 2 3]", row[5], row[5])
	}
	if d, ok := row[6].(time.Time); !ok || !d.Equal(time.Date(2020, 1, 31, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("date = %v (%T), want 2020-01-31", row[6], row[6])
	}
	if ts, ok := row[7].(time.Time); !ok || !ts.Equal(time.Date(2020, 1, 31, 12, 30, 0, 0, time.UTC)) {
		t.Errorf("timestamp = %v (%T), want 2020-01-31T12:30:00Z", row[7], row[7])
	}

	// Node decode.
	row = execOne(t, s, "MATCH (p:Person {id: 2}) RETURN p", nil)
	n, ok := row[0].(engine.Node)
	if !ok {
		t.Fatalf("node = %v (%T), want engine.Node", row[0], row[0])
	}
	if len(n.Labels) != 1 || n.Labels[0] != "Person" {
		t.Errorf("node labels = %v, want [Person]", n.Labels)
	}
	if n.Props["name"] != "bob" || n.Props["id"] != int64(2) {
		t.Errorf("node props = %v, want name=bob id=2", n.Props)
	}
	if n.ID == "" {
		t.Error("node ID empty")
	}

	// Rel decode.
	row = execOne(t, s, "MATCH (:Person {id: 1})-[k:KNOWS]->(:Person) RETURN k", nil)
	r, ok := row[0].(engine.Rel)
	if !ok {
		t.Fatalf("rel = %v (%T), want engine.Rel", row[0], row[0])
	}
	if r.Type != "KNOWS" {
		t.Errorf("rel type = %q, want KNOWS", r.Type)
	}
	if r.Props["since"] != int64(2010) {
		t.Errorf("rel props = %v, want since=2010", r.Props)
	}
	if r.Start == "" || r.End == "" {
		t.Errorf("rel endpoints empty: start=%q end=%q", r.Start, r.End)
	}
}

// TestTransactionProbe logs the probe result and exercises whichever
// branch it declared: real BEGIN/COMMIT/ROLLBACK or ErrNoTransactions.
func TestTransactionProbe(t *testing.T) {
	e := New()
	s := startSession(t, e)
	ctx := context.Background()
	if _, err := s.Load(ctx, newFixture(t)); err != nil {
		t.Fatalf("Load: %v", err)
	}

	info := e.Info()
	t.Logf("transaction probe: Transactions=%v Algorithms=%v", info.Caps.Transactions, info.Caps.Algorithms)

	tx, err := s.Begin(ctx, engine.WriteMode)
	if !info.Caps.Transactions {
		if !errors.Is(err, engine.ErrNoTransactions) {
			t.Fatalf("Begin err = %v, want ErrNoTransactions", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("Begin: %v (probe declared Transactions=true)", err)
	}
	if _, err := tx.Exec(ctx, engine.Op{Text: "CREATE (:Person {id: 99, name: 'tx', age: 1})"}); err != nil {
		t.Fatalf("tx Exec: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	row := execOne(t, s, "MATCH (p:Person) RETURN count(*) AS n", nil)
	if n := row[0].(int64); n != 3 {
		t.Errorf("count after rollback = %d, want 3 (rollback did not undo the write)", n)
	}

	tx, err = s.Begin(ctx, engine.WriteMode)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := tx.Exec(ctx, engine.Op{Text: "CREATE (:Person {id: 100, name: 'tx2', age: 2})"}); err != nil {
		t.Fatalf("tx Exec: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	row = execOne(t, s, "MATCH (p:Person) RETURN count(*) AS n", nil)
	if n := row[0].(int64); n != 4 {
		t.Errorf("count after commit = %d, want 4", n)
	}
}

// TestCloseIdempotent closes twice; the second must be a no-op.
func TestCloseIdempotent(t *testing.T) {
	e := New()
	s := startSession(t, e)
	ctx := context.Background()
	if err := s.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := s.Exec(ctx, engine.Op{Text: "RETURN 1"}); err == nil {
		t.Fatal("Exec after Close succeeded, want error")
	}
}

// TestInfo checks the static identity fields.
func TestInfo(t *testing.T) {
	info := New().Info()
	if info.Name != "ladybug" {
		t.Errorf("Name = %q", info.Name)
	}
	if info.Plane != engine.InProc {
		t.Errorf("Plane = %q", info.Plane)
	}
	if len(info.Dialects) != 2 || info.Dialects[0] != engine.KuzuCy || info.Dialects[1] != engine.Cypher {
		t.Errorf("Dialects = %v, want [kuzu cypher]", info.Dialects)
	}
	caps := info.Caps
	if !caps.BulkLoad || !caps.Deletes || !caps.VarLengthPaths || !caps.ShortestPaths || !caps.Persistent {
		t.Errorf("Caps = %+v, want BulkLoad/Deletes/VarLengthPaths/ShortestPaths/Persistent all true", caps)
	}
	if caps.MaxConcurrency < 1 {
		t.Errorf("MaxConcurrency = %d, want >= 1", caps.MaxConcurrency)
	}
}
