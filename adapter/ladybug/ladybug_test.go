//go:build ladybug

package ladybug

import (
	"context"
	"os"
	"testing"

	"github.com/tamnd/graph-bench/target"
)

// TestSetupLoadRunTeardown opens a temp LadybugDB database, loads a small
// graph via statements, runs a traversal count, and verifies the result.
func TestSetupLoadRunTeardown(t *testing.T) {
	ctx := context.Background()

	tmp, err := os.MkdirTemp("", "ladybug-test-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })

	tg := New()

	if tg.Name() != "ladybug" {
		t.Fatalf("Name = %q, want ladybug", tg.Name())
	}
	if tg.Plane() != target.InProc {
		t.Fatalf("Plane = %v, want InProc", tg.Plane())
	}

	cfg := target.Config{Values: map[string]string{"path": tmp + "/test.db"}}
	drv, err := tg.Setup(ctx, cfg)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = tg.Teardown(ctx, drv) })

	ds := target.NewStatements("tiny", []string{
		`CREATE NODE TABLE Node (id INT64, name STRING, PRIMARY KEY(id))`,
		`CREATE REL TABLE EDGE (FROM Node TO Node)`,
		`CREATE (:Node {id: 1, name: 'a'})`,
		`CREATE (:Node {id: 2, name: 'b'})`,
		`CREATE (:Node {id: 3, name: 'c'})`,
		`MATCH (a:Node {id: 1}), (b:Node {id: 2}) CREATE (a)-[:EDGE]->(b)`,
		`MATCH (a:Node {id: 2}), (b:Node {id: 3}) CREATE (a)-[:EDGE]->(b)`,
		`MATCH (a:Node {id: 3}), (b:Node {id: 1}) CREATE (a)-[:EDGE]->(b)`,
	})

	if _, err := tg.Load(ctx, drv, ds); err != nil {
		t.Fatalf("Load: %v", err)
	}

	q := target.Query{
		ID:    "tiny-count",
		Class: target.Traversal,
		Text:  `MATCH (a)-[:EDGE]->(b) RETURN count(*) AS n`,
	}
	res, err := drv.Run(ctx, q, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	cols := res.Columns()
	if len(cols) != 1 || cols[0] != "n" {
		t.Errorf("Columns = %v, want [n]", cols)
	}
	if !res.Next() {
		t.Fatal("no rows returned")
	}
	row := res.Row()
	if len(row) != 1 {
		t.Fatalf("row len = %d, want 1", len(row))
	}
	n, ok := row[0].(int64)
	if !ok {
		t.Fatalf("value is %T (%v), want int64", row[0], row[0])
	}
	if n != 3 {
		t.Errorf("edge count = %d, want 3", n)
	}
	if err := res.Err(); err != nil {
		t.Fatalf("result error: %v", err)
	}
	if err := res.Close(); err != nil {
		t.Fatalf("result close: %v", err)
	}
}

// TestBeginReturnsError checks that Begin returns errTxNotSupported.
func TestBeginReturnsError(t *testing.T) {
	ctx := context.Background()
	tmp, err := os.MkdirTemp("", "ladybug-tx-test-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })

	tg := New()
	cfg := target.Config{Values: map[string]string{"path": tmp + "/tx.db"}}
	drv, err := tg.Setup(ctx, cfg)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = tg.Teardown(ctx, drv) })

	_, err = drv.Begin(ctx, target.WriteMode)
	if err == nil {
		t.Fatal("Begin returned nil error, want errTxNotSupported")
	}
}

// TestVersion checks that Version returns a non-empty string.
func TestVersion(t *testing.T) {
	tg := New()
	v, err := tg.Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v == "" {
		t.Fatal("Version returned empty string")
	}
	t.Logf("LadybugDB version: %s", v)
}
