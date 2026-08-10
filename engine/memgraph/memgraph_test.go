//go:build bolt

package memgraph

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
	if info.Name != "memgraph" || info.Plane != engine.Bolt {
		t.Errorf("identity: %+v", info)
	}
	// MAGE first, then Cypher: the analytics texts are written against
	// Memgraph's procedure library, and this is the only engine that has it.
	if len(info.Dialects) != 2 || info.Dialects[0] != engine.MAGE || info.Dialects[1] != engine.Cypher {
		t.Errorf("dialects: %v", info.Dialects)
	}
	c := info.Caps
	if !c.Transactions || !c.BulkLoad || !c.Deletes || !c.VarLengthPaths || !c.ShortestPaths {
		t.Errorf("caps: %+v", c)
	}
	if c.Persistent {
		t.Error("memgraph is in-memory by default; Persistent must be false")
	}
	for _, algo := range []string{"pagerank", "bfs", "wcc", "sssp", "bc", "cdlp"} {
		if !c.HasAlgorithm(algo) {
			t.Errorf("missing MAGE algorithm %q", algo)
		}
	}
}

func TestResolveURI(t *testing.T) {
	t.Setenv("MEMGRAPH_URI", "")
	if got := resolveURI(engine.Config{}); got != "bolt://127.0.0.1:7688" {
		t.Errorf("default uri: %q", got)
	}

	t.Setenv("MEMGRAPH_URI", "bolt://envhost:7777")
	if got := resolveURI(engine.Config{}); got != "bolt://envhost:7777" {
		t.Errorf("env uri: %q", got)
	}

	cfg := engine.Config{Values: map[string]string{"uri": "bolt://cfg:1"}}
	if got := resolveURI(cfg); got != "bolt://cfg:1" {
		t.Errorf("explicit uri wins: %q", got)
	}
}

func TestIndexStatement(t *testing.T) {
	if got := indexStatement("Node"); got != "CREATE INDEX ON :Node(id)" {
		t.Errorf("index statement: %q", got)
	}
}

func TestBuildUnwindCypherNode(t *testing.T) {
	cols := bolt.ParseHeader("id:ID,:LABEL", nil)
	got := buildUnwindCypher([]string{"0,Node", "1,Node"}, cols, "Node", true, "", "")
	for _, want := range []string{
		"UNWIND [{id:0},{id:1}] AS row",
		"CREATE (n:Node) SET n = row",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestBuildUnwindCypherRel(t *testing.T) {
	cols := bolt.ParseHeader(":START_ID,:END_ID,:TYPE", nil)
	got := buildUnwindCypher([]string{"0,1,EDGE"}, cols, "EDGE", false, "Node", "Node")
	for _, want := range []string{
		"UNWIND [{__s:0,__e:1}] AS row",
		"MATCH (a:Node {id: row.__s}) MATCH (b:Node {id: row.__e}) CREATE (a)-[r:EDGE]->(b)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestBuildUnwindCypherRelProps(t *testing.T) {
	cols := bolt.ParseHeader(":START_ID,:END_ID,weight:FLOAT64", nil)
	got := buildUnwindCypher([]string{"5,6,1.5"}, cols, "EDGE", false, "A", "B")
	for _, want := range []string{
		"{__s:5,__e:6,weight:1.5}",
		"SET r.weight = row.weight",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestBuildUnwindCypherEmptyAndStrings(t *testing.T) {
	cols := bolt.ParseHeader("id:ID,name:STRING", nil)
	if got := buildUnwindCypher(nil, cols, "Node", true, "", ""); got != "" {
		t.Errorf("empty batch: got %q", got)
	}
	got := buildUnwindCypher([]string{`7,a"b`}, cols, "Node", true, "", "")
	if !strings.Contains(got, `{id:7,name:"a\"b"}`) {
		t.Errorf("string escaping:\n%s", got)
	}
	got = buildUnwindCypher([]string{"7,"}, cols, "Node", true, "", "")
	if !strings.Contains(got, "name:null") {
		t.Errorf("empty field should encode as null:\n%s", got)
	}
}

func TestUnwindBatching(t *testing.T) {
	// Pure check that the batch arithmetic covers every row exactly once.
	total := 2*unwindBatchSize + 17
	var covered int
	for i := 0; i < total; i += unwindBatchSize {
		end := min(i+unwindBatchSize, total)
		covered += end - i
	}
	if covered != total {
		t.Errorf("batching covered %d of %d rows", covered, total)
	}
}

// TestIntegration exercises the full Start/Load/Exec/Close path against a
// live Memgraph. Guarded: set GRAPH_BENCH_BOLT_IT=1 (and optionally
// MEMGRAPH_URI) to run.
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
	t.Logf("memgraph version: %s", v)

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
}
