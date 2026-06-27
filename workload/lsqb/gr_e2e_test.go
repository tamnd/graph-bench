package lsqb_test

import (
	"context"
	"testing"

	gradapter "github.com/tamnd/graph-bench/adapter/gr"
	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
	"github.com/tamnd/graph-bench/workload/lsqb"
)

// genSNB materializes a small canonical dataset on the SNB schema: the eight node
// labels and ten relationship types the nine LSQB queries touch, sized so every
// query has a non-trivial non-zero count. Node ids are globally unique across
// labels (the LDBC convention the oracles rely on), so an edge resolves to one
// node regardless of label. It returns the dataset opened for reading, the same
// target.Dataset an engine and the oracle both consume.
//
// The KNOWS edges form two triangles ({1,2,3} and {1,3,4}) and a four-cycle
// (1-2-3-4), so Q5, Q6, Q7, and Q9 all match. A deliberately spurious Forum->Tag
// HAS_TAG edge (601->401) exercises both gr's label filter and the oracle's
// join-pinning: it must not be counted as a message's tag in Q2, Q4, Q6, or Q9.
func genSNB(t *testing.T) target.Dataset {
	t.Helper()
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	nodeHeader := []target.Column{{Name: "id", Type: "ID"}}
	relHeader := []target.Column{{Type: "START_ID"}, {Type: "END_ID"}}

	addNodes := func(label string, ids ...string) {
		rw, err := w.NodeFile(label, nodeHeader)
		if err != nil {
			t.Fatalf("NodeFile %s: %v", label, err)
		}
		for _, id := range ids {
			if err := rw.Write([]string{id}); err != nil {
				t.Fatalf("write %s node: %v", label, err)
			}
		}
		if err := rw.Close(); err != nil {
			t.Fatalf("close %s: %v", label, err)
		}
	}
	addRels := func(typ string, edges [][2]string) {
		rw, err := w.RelFile(typ, relHeader)
		if err != nil {
			t.Fatalf("RelFile %s: %v", typ, err)
		}
		for _, e := range edges {
			if err := rw.Write([]string{e[0], e[1]}); err != nil {
				t.Fatalf("write %s edge: %v", typ, err)
			}
		}
		if err := rw.Close(); err != nil {
			t.Fatalf("close %s: %v", typ, err)
		}
	}

	addNodes("Person", "1", "2", "3", "4")
	addNodes("City", "101", "102")
	addNodes("Country", "201")
	addNodes("University", "301", "302")
	addNodes("Tag", "401", "402")
	addNodes("TagClass", "501")
	addNodes("Forum", "601", "602")
	addNodes("Message", "1001", "1002", "1003", "1004", "1005", "1006")

	// KNOWS, stored once per undirected pair; the queries match it undirected.
	addRels("KNOWS", [][2]string{{"1", "2"}, {"2", "3"}, {"3", "1"}, {"3", "4"}, {"4", "1"}})
	addRels("IS_LOCATED_IN", [][2]string{{"1", "101"}, {"2", "101"}, {"3", "102"}, {"4", "102"}})
	addRels("IS_PART_OF", [][2]string{{"101", "201"}, {"102", "201"}})
	addRels("STUDY_AT", [][2]string{{"1", "301"}, {"2", "302"}, {"3", "301"}, {"4", "302"}})
	addRels("HAS_CREATOR", [][2]string{
		{"1001", "1"}, {"1002", "1"}, {"1003", "2"}, {"1004", "3"}, {"1005", "4"}, {"1006", "2"},
	})
	addRels("HAS_TAG", [][2]string{
		{"1001", "401"}, {"1002", "401"}, {"1003", "401"}, {"1004", "401"}, {"1005", "401"}, {"1006", "402"},
		{"601", "401"}, // spurious Forum->Tag: must not count as a message tag
	})
	addRels("HAS_TYPE", [][2]string{{"401", "501"}, {"402", "501"}})
	addRels("HAS_MODERATOR", [][2]string{{"601", "1"}, {"602", "2"}})
	addRels("HAS_MEMBER", [][2]string{
		{"601", "1"}, {"601", "2"}, {"601", "3"}, {"602", "1"}, {"602", "2"}, {"602", "3"},
	})
	addRels("CONTAINER_OF", [][2]string{{"601", "1001"}, {"601", "1003"}, {"602", "1004"}})

	if _, err := w.Finalize(&target.Manifest{Name: "snb-mini"}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ds
}

// TestGrLSQBAgainstOracle is the end-to-end proof for the LSQB workload: it loads
// the small SNB dataset into gr through the bulk path, runs every LSQB query, and
// checks gr's count against the engine-independent oracle wired into the query's
// reference. It closes the gap that the oracle unit tests leave open, that the
// oracle agrees with brute force but was never run against the engine it
// validates. It needs no LDBC download, so it runs in CI and is the cheapest
// place to catch an oracle or an engine drift.
func TestGrLSQBAgainstOracle(t *testing.T) {
	ds := genSNB(t)
	wl, ok := workload.Lookup("lsqb")
	if !ok {
		t.Fatal("workload lsqb not registered")
	}

	tgt := gradapter.New()
	ctx := context.Background()
	drv, err := tgt.Setup(ctx, target.Config{Values: map[string]string{"path": t.TempDir() + "/lsqb.gr"}})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer func() { _ = tgt.Teardown(ctx, drv) }()
	if _, err := tgt.Load(ctx, drv, ds); err != nil {
		t.Fatalf("Load: %v", err)
	}

	for _, q := range wl.Queries {
		q := q
		t.Run(q.ID, func(t *testing.T) {
			if q.Reference.Compute == nil {
				t.Fatalf("%s has nil Compute; every LSQB query must validate", q.ID)
			}
			params := q.Params.Next()
			want, err := q.Reference.Compute(ds, params)
			if err != nil {
				t.Fatalf("%s oracle: %v", q.ID, err)
			}
			// Guard against a degenerate fixture: a query that counts zero would pass
			// vacuously. Every LSQB pattern is built to match on this dataset.
			if n, ok := want.Rows[0][0].(int64); ok && n == 0 {
				t.Fatalf("%s oracle counts zero on the fixture; the fixture should exercise it", q.ID)
			}

			text := q.Texts[workload.Cypher]
			got := runGrCount(t, drv, ctx, text, params, want.Columns)
			if err := workload.Compare(got, want, q.Reference.Compare); err != nil {
				t.Errorf("%s: gr count does not match the oracle: %v\n  gr=%v oracle=%v",
					q.ID, err, got.Rows, want.Rows)
			}
		})
	}
}

// runGrCount runs one count query and returns its single-row answer.
func runGrCount(t *testing.T, drv target.Driver, ctx context.Context, text string, params target.Params, cols []string) *target.Answer {
	t.Helper()
	res, err := drv.Run(ctx, target.Query{Text: text, Class: target.Subgraph}, params)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var rows [][]target.Value
	for res.Next() {
		src := res.Row()
		row := make([]target.Value, len(src))
		copy(row, src)
		rows = append(rows, row)
	}
	if err := res.Err(); err != nil {
		t.Fatalf("iter: %v", err)
	}
	_ = res.Close()
	return &target.Answer{Columns: cols, Rows: rows}
}

// keep lsqb imported for the package's exported oracle entry point, so this file
// stays coupled to the package it validates even as the test drives through the
// workload registry.
var _ = lsqb.CountOracle
