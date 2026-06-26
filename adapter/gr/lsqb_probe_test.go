package gr

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/target"
)

// TestLSQBProbe loads a canonical LDBC dataset directory once through the gr bulk
// path and runs each LSQB count query with a per-query timeout, logging the count
// and wall time. It is a manual probe over the real dataset (the repacked SF0.1 or
// SF1 tree), skipped unless GR_LDBC_DATASET names a canonical dataset directory.
//
// It isolates which LSQB pattern is expensive on real SNB data without sitting
// through the steady-state harness, so a slow query can be profiled in isolation
// rather than guessed at. A query that does not finish inside the timeout is logged
// as such and the probe moves on, so one runaway pattern does not block the rest.
func TestLSQBProbe(t *testing.T) {
	dir := os.Getenv("GR_LDBC_DATASET")
	if dir == "" {
		t.Skip("set GR_LDBC_DATASET to a canonical dataset directory")
	}
	timeout := 90 * time.Second
	if v := os.Getenv("GR_LSQB_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			timeout = d
		}
	}

	ctx := context.Background()
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("open dataset: %v", err)
	}

	tg := New()
	path := filepath.Join(t.TempDir(), "lsqb.gr")
	drv, err := tg.Setup(ctx, target.Config{Values: map[string]string{"path": path}})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Cleanup(func() { _ = tg.Teardown(ctx, drv) })

	loadStart := time.Now()
	stats, err := tg.Load(ctx, drv, ds)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Logf("loaded %d nodes, %d edges in %s", stats.Nodes, stats.Edges, time.Since(loadStart).Round(time.Millisecond))

	// id sanity: the repack carries the raw LDBC id as an int property, and the
	// adapter must expose it as the integer the {id: $param} short reads match, not
	// the prefixed :ID string. Pull one Person id, confirm it is an int64, then look
	// it up by that integer and confirm the round trip finds exactly that node.
	idRes, err := drv.Run(ctx, target.Query{ID: "id-pick", Class: target.PointRead,
		Text: `MATCH (p:Person) RETURN p.id AS id ORDER BY id LIMIT 1`}, nil)
	if err != nil {
		t.Fatalf("id pick: %v", err)
	}
	var personID int64
	if idRes.Next() {
		v := idRes.Row()[0]
		got, ok := v.(int64)
		if !ok {
			t.Fatalf("Person.id is %T (%v), want int64", v, v)
		}
		personID = got
	}
	_ = idRes.Close()
	t.Logf("sample Person.id = %d (int64)", personID)

	lookup, err := drv.Run(ctx, target.Query{ID: "id-lookup", Class: target.PointRead,
		Text: `MATCH (p:Person {id: $personId}) RETURN p.id AS id`}, target.Params{"personId": personID})
	if err != nil {
		t.Fatalf("id lookup: %v", err)
	}
	var found int64 = -1
	if lookup.Next() {
		if v, ok := lookup.Row()[0].(int64); ok {
			found = v
		}
	}
	_ = lookup.Close()
	if found != personID {
		t.Fatalf("id lookup round trip: found %d, want %d", found, personID)
	}
	t.Logf("id lookup round trip ok: {id: %d} -> %d", personID, found)

	queries := []struct{ id, text string }{
		{"q1", `MATCH (p:Person)-[:IS_LOCATED_IN]->(:City)-[:IS_PART_OF]->(:Country) RETURN count(*) AS cnt`},
		{"q2", `MATCH (m:Message)-[:HAS_CREATOR]->(p:Person)-[:IS_LOCATED_IN]->(:City) RETURN count(*) AS cnt`},
		{"q3", `MATCH (f:Forum)-[:HAS_MODERATOR]->(:Person) RETURN count(*) AS cnt`},
		{"q4", `MATCH (m:Message)-[:HAS_TAG]->(:Tag)-[:HAS_TYPE]->(:TagClass) RETURN count(*) AS cnt`},
		{"q5", `MATCH (a:Person)-[:KNOWS]-(b:Person)-[:KNOWS]-(c:Person)-[:KNOWS]-(a) RETURN count(*) AS cnt`},
		{"q7", `MATCH (a:Person)-[:KNOWS]-(b:Person)-[:KNOWS]-(c:Person)-[:KNOWS]-(d:Person)-[:KNOWS]-(a) RETURN count(*) AS cnt`},
		{"q8", `MATCH (p:Person)<-[:HAS_CREATOR]-(m1:Message)-[:HAS_TAG]->(t:Tag), (p)<-[:HAS_CREATOR]-(m2:Message)-[:HAS_TAG]->(t) WHERE m1 <> m2 RETURN count(*) AS cnt`},
	}

	for _, q := range queries {
		runOne(t, drv, q.id, q.text, timeout)
	}
}

// runOne runs a single query under a timeout in a goroutine, logging count and
// elapsed time, or that it exceeded the timeout. The query keeps running in the
// background goroutine after a timeout (gr has no mid-query cancellation here), but
// the probe does not wait on it.
func runOne(t *testing.T, drv target.Driver, id, text string, timeout time.Duration) {
	t.Helper()
	type outcome struct {
		count   int64
		elapsed time.Duration
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		start := time.Now()
		res, err := drv.Run(context.Background(), target.Query{ID: id, Class: target.Subgraph, Text: text}, nil)
		if err != nil {
			done <- outcome{err: err}
			return
		}
		var c int64
		if res.Next() {
			if v, ok := res.Row()[0].(int64); ok {
				c = v
			}
		}
		err = res.Err()
		_ = res.Close()
		done <- outcome{count: c, elapsed: time.Since(start), err: err}
	}()

	select {
	case o := <-done:
		if o.err != nil {
			t.Logf("%s: ERROR %v", id, o.err)
			return
		}
		t.Logf("%s: count=%d in %s", id, o.count, o.elapsed.Round(time.Millisecond))
	case <-time.After(timeout):
		t.Logf("%s: TIMEOUT after %s (still running)", id, timeout)
	}
}
