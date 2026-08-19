package mongo

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/engine/sqlbase/sqltest"
)

// These tests need a server, and a unit test has no business starting
// one: a container takes tens of seconds and `go test ./...` should stay
// fast and offline. So they run against whatever server the operator
// points them at and skip otherwise, which is the same discovery order
// the adapter itself uses.
//
//	docker run --rm -d -p 27017:27017 mongo:8.3.8
//	GRAPH_BENCH_MONGO_URI=mongodb://127.0.0.1:27017 go test ./engine/mongo/
//
// What they check is the adapter, not the workload: the load, the
// parameter binding through let, the declared column order, and the
// decoding of a boolean and a count. The workload's own pipelines are
// checked by the run verb, which verifies every answer against the
// harness's oracle before it times anything.
func requireServer(t *testing.T) {
	t.Helper()
	if os.Getenv("GRAPH_BENCH_MONGO_URI") == "" && os.Getenv("MONGODB_URI") == "" {
		t.Skip("no MongoDB server: set GRAPH_BENCH_MONGO_URI or MONGODB_URI")
	}
}

// The graph sqltest builds: a triangle 1->2->3->1 with a tail 3->4.
func loaded(t *testing.T) engine.Session {
	t.Helper()
	requireServer(t)
	ctx := context.Background()
	sess, err := New().Start(ctx, engine.Config{Values: map[string]string{}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { sess.Close(context.Background()) })
	stats, err := sess.Load(ctx, sqltest.Dataset(t))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if stats.Nodes != 4 || stats.Edges != 4 {
		t.Errorf("loaded %d nodes and %d edges, want 4 and 4", stats.Nodes, stats.Edges)
	}
	if stats.Method != "insert-many" {
		t.Errorf("load method %q, want insert-many", stats.Method)
	}
	if stats.Duration <= 0 {
		t.Errorf("load duration %v, want a positive duration", stats.Duration)
	}
	if stats.BytesOnDisk <= 0 {
		t.Errorf("load reported %d bytes stored, want a footprint", stats.BytesOnDisk)
	}
	return sess
}

func TestVersion(t *testing.T) {
	sess := loaded(t)
	v, err := sess.Version(context.Background())
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.HasPrefix(v, "8.") {
		t.Errorf("version %q does not look like the pinned MongoDB", v)
	}
}

// rows runs one pipeline and collects it, which is what every case below
// needs and what the runner does with a Result.
func rows(t *testing.T, sess engine.Session, text string, params map[string]engine.Value) ([]string, [][]engine.Value) {
	t.Helper()
	res, err := sess.Exec(context.Background(), engine.Op{
		QueryID: t.Name(),
		Class:   engine.PointRead,
		Dialect: engine.Mongo,
		Text:    text,
		Params:  params,
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	defer res.Close()
	cols := res.Columns()
	var out [][]engine.Value
	for res.Next() {
		row := make([]engine.Value, len(res.Row()))
		copy(row, res.Row())
		out = append(out, row)
	}
	if err := res.Err(); err != nil {
		t.Fatalf("result: %v", err)
	}
	return cols, out
}

const pointText = `{"collection": "node", "columns": ["id"],
 "pipeline": [{"$match": {"$expr": {"$eq": ["$_id", {"$toLong": "$$id"}]}}},
              {"$project": {"_id": 0, "id": "$_id"}}]}`

func TestPointRead(t *testing.T) {
	sess := loaded(t)
	cols, got := rows(t, sess, pointText, map[string]engine.Value{"id": "2"})
	if len(cols) != 1 || cols[0] != "id" {
		t.Fatalf("columns %v, want [id]", cols)
	}
	if len(got) != 1 || got[0][0] != int64(2) {
		t.Fatalf("got %v, want one row holding int64(2)", got)
	}
}

// A miss is no rows, not a row of nulls. It is the same distinction the
// relational suite checks, and it is where an adapter that invents a
// default row gets caught.
func TestPointMiss(t *testing.T) {
	sess := loaded(t)
	if _, got := rows(t, sess, pointText, map[string]engine.Value{"id": "99"}); len(got) != 0 {
		t.Fatalf("got %v, want no rows", got)
	}
}

const edgeText = `{"collection": "node", "columns": ["found"],
 "pipeline": [{"$match": {"$expr": {"$eq": ["$_id", {"$toLong": "$$src"}]}}},
              {"$lookup": {"from": "edge", "let": {"s": "$_id", "d": {"$toLong": "$$dst"}},
                           "pipeline": [{"$match": {"$expr": {"$and": [{"$eq": ["$src", "$$s"]},
                                                                      {"$eq": ["$dst", "$$d"]}]}}},
                                        {"$limit": 1}],
                           "as": "hit"}},
              {"$project": {"_id": 0, "found": {"$gt": [{"$size": "$hit"}, 0]}}}]}`

// The probe answers a real boolean, and an absent edge answers false
// rather than no rows.
func TestEdgeProbe(t *testing.T) {
	sess := loaded(t)
	for _, c := range []struct {
		src, dst string
		want     bool
	}{
		{"1", "2", true},
		{"1", "3", false},
	} {
		_, got := rows(t, sess, edgeText, map[string]engine.Value{"src": c.src, "dst": c.dst})
		if len(got) != 1 {
			t.Fatalf("(%s,%s): got %d rows, want 1", c.src, c.dst, len(got))
		}
		if got[0][0] != c.want {
			t.Errorf("(%s,%s): got %#v, want %v", c.src, c.dst, got[0][0], c.want)
		}
	}
}

// A walk of one to three hops out of 1 reaches 2, then 3, then 1 and 4.
// $graphLookup is what answers it, and this is the case that would fail
// if maxDepth counted nodes instead of edges.
func TestGraphLookupWalk(t *testing.T) {
	sess := loaded(t)
	const text = `{"collection": "node", "columns": ["n"],
 "pipeline": [{"$match": {"$expr": {"$eq": ["$_id", {"$toLong": "$$seed"}]}}},
              {"$graphLookup": {"from": "edge", "startWith": "$_id", "connectFromField": "dst",
                                "connectToField": "src", "maxDepth": 2, "as": "reach"}},
              {"$project": {"_id": 0, "n": {"$size": {"$setUnion": ["$reach.dst", []]}}}}]}`
	_, got := rows(t, sess, text, map[string]engine.Value{"seed": "1"})
	if len(got) != 1 || got[0][0] != int64(4) {
		t.Fatalf("got %v, want one row holding int64(4)", got)
	}
}

// A count comes back from the server as a 32-bit integer and has to reach
// the value model as an int64, because a count is a count whatever width
// the wire used.
func TestScanCountIsInt64(t *testing.T) {
	sess := loaded(t)
	_, got := rows(t, sess, `{"collection": "node", "columns": ["n"], "pipeline": [{"$count": "n"}]}`, nil)
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if got[0][0] != int64(4) {
		t.Fatalf("got %#v, want int64(4)", got[0][0])
	}
}

// Two columns, in the order the text declares them, whatever order the
// server put the fields in.
func TestColumnOrder(t *testing.T) {
	sess := loaded(t)
	const text = `{"collection": "node", "columns": ["n", "avgId"],
 "pipeline": [{"$group": {"_id": null, "n": {"$sum": 1}, "avgId": {"$avg": "$_id"}}},
              {"$project": {"_id": 0, "n": 1, "avgId": 1}}]}`
	cols, got := rows(t, sess, text, nil)
	if len(cols) != 2 || cols[0] != "n" || cols[1] != "avgId" {
		t.Fatalf("columns %v, want [n avgId]", cols)
	}
	if len(got) != 1 || got[0][0] != int64(4) || got[0][1] != 2.5 {
		t.Fatalf("got %v, want one row of int64(4) and 2.5", got)
	}
}

// A dataset the two collections cannot hold is refused, not merged.
func TestRefusesMultiLabel(t *testing.T) {
	requireServer(t)
	sess, err := New().Start(context.Background(), engine.Config{Values: map[string]string{}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sess.Close(context.Background())
	_, err = sess.Load(context.Background(), sqltest.TwoLabelDataset(t))
	if err == nil {
		t.Fatal("loaded a two-label dataset, want a refusal")
	}
	if !strings.Contains(err.Error(), "2 labels") {
		t.Errorf("error %q does not say the dataset has 2 labels", err)
	}
}

func TestBeginRefuses(t *testing.T) {
	sess := loaded(t)
	if _, err := sess.Begin(context.Background(), engine.ReadMode); err != engine.ErrNoTransactions {
		t.Fatalf("begin returned %v, want ErrNoTransactions", err)
	}
}
