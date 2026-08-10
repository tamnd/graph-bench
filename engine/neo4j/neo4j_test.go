//go:build bolt

package neo4j

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/engine/bolt"
)

func TestInfo(t *testing.T) {
	info := New().Info()
	if info.Name != "neo4j" || info.Plane != engine.Bolt {
		t.Errorf("identity: %+v", info)
	}
	wantDialects := []engine.Dialect{engine.Cypher25, engine.Cypher}
	if len(info.Dialects) != 2 || info.Dialects[0] != wantDialects[0] || info.Dialects[1] != wantDialects[1] {
		t.Errorf("dialects: %v, want %v", info.Dialects, wantDialects)
	}
	c := info.Caps
	if !c.Transactions || !c.BulkLoad || !c.Deletes || !c.VarLengthPaths || !c.ShortestPaths {
		t.Errorf("caps should all be true: %+v", c)
	}
	if c.Algorithms != nil || c.MaxConcurrency != 0 || !c.Persistent {
		t.Errorf("caps: %+v", c)
	}
}

func TestResolveConfigDefaults(t *testing.T) {
	t.Setenv("NEO4J_URI", "")
	t.Setenv("NEO4J_USER", "")
	t.Setenv("NEO4J_PASS", "")
	c := resolveConfig(engine.Config{})
	if c.URI != "bolt://127.0.0.1:7687" || c.User != "neo4j" || c.Pass != "" || c.DB != "neo4j" || c.Pool != 64 {
		t.Errorf("defaults: %+v", c)
	}
}

func TestResolveConfigEnv(t *testing.T) {
	t.Setenv("NEO4J_URI", "bolt://envhost:9999")
	t.Setenv("NEO4J_USER", "envuser")
	t.Setenv("NEO4J_PASS", "envpass")
	c := resolveConfig(engine.Config{})
	if c.URI != "bolt://envhost:9999" || c.User != "envuser" || c.Pass != "envpass" {
		t.Errorf("env fallback: %+v", c)
	}
}

func TestResolveConfigExplicitWinsOverEnv(t *testing.T) {
	t.Setenv("NEO4J_URI", "bolt://envhost:9999")
	t.Setenv("NEO4J_PASS", "envpass")
	c := resolveConfig(engine.Config{Values: map[string]string{
		"uri":      "bolt://cfg:1",
		"pass":     "cfgpass",
		"database": "bench",
		"pool":     "8",
	}})
	if c.URI != "bolt://cfg:1" || c.Pass != "cfgpass" || c.DB != "bench" || c.Pool != 8 {
		t.Errorf("explicit config: %+v", c)
	}
}

func TestResolveConfigBadPool(t *testing.T) {
	c := resolveConfig(engine.Config{Values: map[string]string{"pool": "not-a-number"}})
	if c.Pool != 64 {
		t.Errorf("bad pool value should default to 64, got %d", c.Pool)
	}
}

func TestDetectImportDirExplicit(t *testing.T) {
	dir := t.TempDir()
	got := detectImportDir(engine.Config{Values: map[string]string{"import_dir": dir}})
	if got != dir {
		t.Errorf("explicit import_dir: got %q, want %q", got, dir)
	}
}

func TestIsWritableDir(t *testing.T) {
	dir := t.TempDir()
	if !isWritableDir(dir) {
		t.Errorf("temp dir should be writable")
	}
	if isWritableDir(dir + "/does-not-exist") {
		t.Errorf("missing dir should not be writable")
	}
	file := dir + "/f"
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if isWritableDir(file) {
		t.Errorf("regular file should not count as writable dir")
	}
}

func TestCheckUnwindScale(t *testing.T) {
	if err := checkUnwindScale(19_800); err != nil {
		t.Errorf("small dataset refused: %v", err)
	}
	if err := checkUnwindScale(maxUnwindEdges); err != nil {
		t.Errorf("boundary refused: %v", err)
	}
	err := checkUnwindScale(maxUnwindEdges + 1)
	if err == nil {
		t.Fatal("want refusal above 10M edges")
	}
	if !strings.Contains(err.Error(), "F2") {
		t.Errorf("refusal should cite F2: %v", err)
	}
}

func nodeCols() []bolt.Column {
	return bolt.ParseHeader("id:ID,:LABEL", nil)
}

func relCols() []bolt.Column {
	return bolt.ParseHeader(":START_ID,:END_ID,:TYPE", nil)
}

func TestBuildLoadCSVCypherNode(t *testing.T) {
	cols := bolt.ParseHeader("id:ID,:LABEL,name:STRING,age:INT64,score:FLOAT64,ok:BOOL,born:DATE,seen:DATETIME,tags:STRING[]", nil)
	got := buildLoadCSVCypher("gb-1.csv", cols, "Person", true, "", "", ";")
	for _, want := range []string{
		"LOAD CSV WITH HEADERS FROM 'file:///gb-1.csv' AS row",
		"CREATE (n:Person {id: toInteger(row.__id)",
		"name: row.name",
		"age: toInteger(row.age)",
		"score: toFloat(row.score)",
		"ok: toBoolean(row.ok)",
		"born: date(row.born)",
		"seen: datetime(row.seen)",
		"tags: split(row.tags, ';')",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestBuildLoadCSVCypherRel(t *testing.T) {
	got := buildLoadCSVCypher("gb-2.csv", relCols(), "EDGE", false, "Node", "Node", ";")
	for _, want := range []string{
		"MATCH (a:Node {id: toInteger(row.__s)})",
		"MATCH (b:Node {id: toInteger(row.__e)})",
		"CREATE (a)-[r:EDGE]->(b)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "{}") {
		t.Errorf("empty prop map should be omitted:\n%s", got)
	}

	withProps := bolt.ParseHeader(":START_ID,:END_ID,weight:FLOAT64", nil)
	got = buildLoadCSVCypher("gb-3.csv", withProps, "EDGE", false, "A", "B", ";")
	if !strings.Contains(got, "CREATE (a)-[r:EDGE {weight: toFloat(row.weight)}]->(b)") {
		t.Errorf("rel props:\n%s", got)
	}
}

func TestBuildUnwindCypherNode(t *testing.T) {
	got := buildUnwindCypher([]string{"0,Node", "1,Node"}, nodeCols(), "Node", true, "", "")
	for _, want := range []string{
		"UNWIND [{id:0},{id:1}] AS row",
		"CREATE (n:Node) SET n = row",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "LABEL") || strings.Contains(got, "Node\"") {
		t.Errorf("structural label column leaked into row map:\n%s", got)
	}
}

func TestBuildUnwindCypherRel(t *testing.T) {
	got := buildUnwindCypher([]string{"0,1,EDGE", "0,100,EDGE"}, relCols(), "EDGE", false, "Node", "Node")
	for _, want := range []string{
		"UNWIND [{__s:0,__e:1},{__s:0,__e:100}] AS row",
		"MATCH (a:Node {id: row.__s}) MATCH (b:Node {id: row.__e}) CREATE (a)-[r:EDGE]->(b)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestBuildUnwindCypherRelProps(t *testing.T) {
	cols := bolt.ParseHeader(":START_ID,:END_ID,weight:FLOAT64,label:STRING", nil)
	got := buildUnwindCypher([]string{`5,6,1.5,a"b`}, cols, "EDGE", false, "A", "B")
	for _, want := range []string{
		`{__s:5,__e:6,weight:1.5,label:"a\"b"}`,
		"MATCH (a:A {id: row.__s}) MATCH (b:B {id: row.__e})",
		"SET r.weight = row.weight",
		"SET r.label = row.label",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestBuildUnwindCypherEmpty(t *testing.T) {
	if got := buildUnwindCypher(nil, nodeCols(), "Node", true, "", ""); got != "" {
		t.Errorf("empty batch: got %q", got)
	}
}

// TestBuildUnwindCypherStringIDs pins the encoding of non-numeric identifiers.
// An :ID column declares intent, not representation: a dataset whose ids are
// opaque strings once produced `{id:A0}`, which Cypher reads as a reference to
// an undefined variable, and the whole SNB/FinBench matrix failed to load.
func TestBuildUnwindCypherStringIDs(t *testing.T) {
	cols := bolt.ParseHeader("id:ID,name:STRING", nil)
	got := buildUnwindCypher([]string{"A0,acct"}, cols, "Account", true, "", "")
	if !strings.Contains(got, `{id:"A0",name:"acct"}`) {
		t.Errorf("non-numeric id must be quoted:\n%s", got)
	}

	rel := bolt.ParseHeader(":START_ID,:END_ID", nil)
	got = buildUnwindCypher([]string{"A0,P1"}, rel, "OWNS", false, "Account", "Person")
	if !strings.Contains(got, `{__s:"A0",__e:"P1"}`) {
		t.Errorf("non-numeric endpoints must be quoted:\n%s", got)
	}
}

// TestCypherLiteralEscaping checks that a value cannot escape its own literal.
// Backslash has to be doubled before quotes are escaped, or a field ending in
// one would consume the closing quote and splice the rest of the batch into
// the string.
func TestCypherLiteralEscaping(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`a"b`, `"a\"b"`},
		{`back\`, `"back\\"`},
		{`\"`, `"\\\""`},
	} {
		if got := cypherLiteral(tc.in, "STRING"); got != tc.want {
			t.Errorf("cypherLiteral(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
	// A declared numeric type still has to prove the field is numeric.
	if got := cypherLiteral("twelve", "INT64"); got != `"twelve"` {
		t.Errorf("non-numeric INT64 field = %s, want a quoted string", got)
	}
	if got := cypherLiteral("maybe", "BOOL"); got != `"maybe"` {
		t.Errorf("non-boolean BOOL field = %s, want a quoted string", got)
	}
}

func TestBuildUnwindCypherNullForMissing(t *testing.T) {
	cols := bolt.ParseHeader("id:ID,age:INT64", nil)
	got := buildUnwindCypher([]string{"3,"}, cols, "Node", true, "", "")
	if !strings.Contains(got, "age:null") {
		t.Errorf("empty numeric field should encode as null:\n%s", got)
	}
}

func TestWriteImportCSV(t *testing.T) {
	dir := t.TempDir()
	path, err := writeImportCSV(dir, relCols(), []string{"0,1,EDGE", "2,3,EDGE"}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "__s,__e\n0,1\n2,3\n"
	if string(content) != want {
		t.Errorf("rel import csv:\n got %q\nwant %q", content, want)
	}

	cols := bolt.ParseHeader("id:ID,:LABEL,name:STRING", nil)
	path, err = writeImportCSV(dir, cols, []string{"0,Node,alice"}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	content, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "__id,name\n0,alice\n" {
		t.Errorf("node import csv: %q", content)
	}
}

// TestIntegration exercises the full Start/Load/Exec/Close path against a
// live Neo4j. Guarded: set GRAPH_BENCH_BOLT_IT=1 (and optionally NEO4J_URI,
// NEO4J_USER, NEO4J_PASS) to run.
func TestIntegration(t *testing.T) {
	if os.Getenv("GRAPH_BENCH_BOLT_IT") != "1" {
		t.Skip("set GRAPH_BENCH_BOLT_IT=1 to run integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	sess, err := New().Start(ctx, engine.Config{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sess.Close(ctx)

	v, err := sess.Version(ctx)
	if err != nil || v == "" {
		t.Fatalf("Version: %q, %v", v, err)
	}
	t.Logf("neo4j version: %s", v)

	ds := engine.NewStatements("it-smoke", engine.Schema{}, []string{
		"CREATE (:Node {id: 1})-[:EDGE]->(:Node {id: 2})",
	})
	stats, err := sess.Load(ctx, ds)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stats.Method != "statements" {
		t.Errorf("Method: %q", stats.Method)
	}

	res, err := sess.Exec(ctx, engine.Op{Class: engine.PointRead, Text: "MATCH (n:Node) RETURN n.id ORDER BY n.id"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	var ids []engine.Value
	for res.Next() {
		ids = append(ids, res.Row()[0])
	}
	if err := res.Err(); err != nil {
		t.Fatalf("stream: %v", err)
	}
	res.Close()
	if len(ids) != 2 || ids[0] != int64(1) || ids[1] != int64(2) {
		t.Errorf("ids: %v", ids)
	}

	tx, err := sess.Begin(ctx, engine.WriteMode)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	r, err := tx.Exec(ctx, engine.Op{Class: engine.Write, Text: "CREATE (:Node {id: 3})"})
	if err != nil {
		t.Fatalf("tx Exec: %v", err)
	}
	for r.Next() {
	}
	r.Close()
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
}
