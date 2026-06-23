package gr

import (
	"context"
	"testing"

	"github.com/tamnd/graph-bench/target"
)

// tinyDataset is a minimal Dataset: a handful of nodes and edges built through
// openCypher statements. The gr in-process adapter loads through statements, so
// this exercises its real load path without needing a materialized CSV dataset.
func tinyDataset() target.Dataset {
	return target.NewStatements("micro-tiny", []string{
		`CREATE (a:Person {name: 'Alice', age: 30})`,
		`CREATE (b:Person {name: 'Bob', age: 25})`,
		`MATCH (a:Person {name: 'Alice'}), (b:Person {name: 'Bob'}) CREATE (a)-[:KNOWS]->(b)`,
	})
}

// TestSetupLoadRunTeardown is the M1 milestone: a measurable query against gr in
// process. It opens an in-memory gr database, loads the tiny graph, runs a point
// read and a one-hop traversal, and checks the rows.
func TestSetupLoadRunTeardown(t *testing.T) {
	ctx := context.Background()
	tg := New()

	if tg.Name() != "gr" {
		t.Fatalf("Name = %q, want gr", tg.Name())
	}
	if tg.Plane() != target.InProc {
		t.Fatalf("Plane = %v, want InProc", tg.Plane())
	}

	drv, err := tg.Setup(ctx, target.Config{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = tg.Teardown(ctx, drv) })

	stats, err := tg.Load(ctx, drv, tinyDataset())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stats.Nodes != 2 {
		t.Errorf("loaded Nodes = %d, want 2", stats.Nodes)
	}
	if stats.Edges != 1 {
		t.Errorf("loaded Edges = %d, want 1", stats.Edges)
	}

	// Point read: fetch Alice's age.
	q := target.Query{ID: "tiny-point", Class: target.PointRead,
		Text: `MATCH (p:Person {name: $name}) RETURN p.age AS age`}
	res, err := drv.Run(ctx, q, target.Params{"name": "Alice"})
	if err != nil {
		t.Fatalf("Run point read: %v", err)
	}
	if got := res.Columns(); len(got) != 1 || got[0] != "age" {
		t.Errorf("Columns = %v, want [age]", got)
	}
	rows := drain(t, res)
	if len(rows) != 1 {
		t.Fatalf("point read returned %d rows, want 1", len(rows))
	}
	if age, ok := rows[0][0].(int64); !ok || age != 30 {
		t.Errorf("age = %v (%T), want int64 30", rows[0][0], rows[0][0])
	}

	// Traversal: who does Alice know.
	q2 := target.Query{ID: "tiny-hop", Class: target.Traversal,
		Text: `MATCH (:Person {name: 'Alice'})-[:KNOWS]->(f) RETURN f.name AS friend`}
	res2, err := drv.Run(ctx, q2, nil)
	if err != nil {
		t.Fatalf("Run traversal: %v", err)
	}
	rows2 := drain(t, res2)
	if len(rows2) != 1 {
		t.Fatalf("traversal returned %d rows, want 1", len(rows2))
	}
	if name, ok := rows2[0][0].(string); !ok || name != "Bob" {
		t.Errorf("friend = %v, want Bob", rows2[0][0])
	}
}

// TestRunReturnsNode checks the graph-object mapping: a returned node arrives as
// a target.Node with its labels and properties in the value model.
func TestRunReturnsNode(t *testing.T) {
	ctx := context.Background()
	tg := New()
	drv, err := tg.Setup(ctx, target.Config{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = tg.Teardown(ctx, drv) })
	if _, err := tg.Load(ctx, drv, tinyDataset()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	q := target.Query{ID: "tiny-node", Class: target.PointRead,
		Text: `MATCH (p:Person {name: 'Bob'}) RETURN p`}
	res, err := drv.Run(ctx, q, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := drain(t, res)
	if len(rows) != 1 {
		t.Fatalf("returned %d rows, want 1", len(rows))
	}
	n, ok := rows[0][0].(target.Node)
	if !ok {
		t.Fatalf("value is %T, want target.Node", rows[0][0])
	}
	if len(n.Labels) != 1 || n.Labels[0] != "Person" {
		t.Errorf("labels = %v, want [Person]", n.Labels)
	}
	if n.Props["name"] != "Bob" {
		t.Errorf("name prop = %v, want Bob", n.Props["name"])
	}
}

// TestTransaction exercises Begin/Run/Commit on the write path.
func TestTransaction(t *testing.T) {
	ctx := context.Background()
	tg := New()
	drv, err := tg.Setup(ctx, target.Config{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = tg.Teardown(ctx, drv) })

	tx, err := drv.Begin(ctx, target.WriteMode)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	q := target.Query{ID: "tiny-write", Class: target.Write,
		Text: `CREATE (:City {name: 'Hanoi'})`}
	res, err := tx.Run(ctx, q, nil)
	if err != nil {
		t.Fatalf("tx Run: %v", err)
	}
	for res.Next() {
	}
	_ = res.Close()
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	res2, err := drv.Run(ctx, target.Query{Text: `MATCH (c:City) RETURN count(c) AS n`}, nil)
	if err != nil {
		t.Fatalf("Run count: %v", err)
	}
	rows := drain(t, res2)
	if len(rows) != 1 || rows[0][0].(int64) != 1 {
		t.Errorf("city count = %v, want 1", rows[0][0])
	}
}

// drain reads a result to completion and returns its rows, failing on a stream
// error. It copies each row so the slice survives the next Next call.
func drain(t *testing.T, res target.Result) [][]target.Value {
	t.Helper()
	var rows [][]target.Value
	for res.Next() {
		src := res.Row()
		row := make([]target.Value, len(src))
		copy(row, src)
		rows = append(rows, row)
	}
	if err := res.Err(); err != nil {
		t.Fatalf("result stream error: %v", err)
	}
	if err := res.Close(); err != nil {
		t.Fatalf("result close: %v", err)
	}
	return rows
}
