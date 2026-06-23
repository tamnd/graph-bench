package lsqb_test

import (
	"strings"
	"testing"

	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
	_ "github.com/tamnd/graph-bench/workload/lsqb"
)

// TestLSQBRegistered proves the "lsqb" workload is in the registry after the
// blank import.
func TestLSQBRegistered(t *testing.T) {
	wl, ok := workload.Lookup("lsqb")
	if !ok {
		t.Fatal("workload lsqb not registered; check init() in lsqb.go")
	}
	if wl.Name != "lsqb" {
		t.Errorf("Name=%q, want lsqb", wl.Name)
	}
	if wl.Dataset == "" {
		t.Error("Dataset should not be empty")
	}
}

// TestLSQBHasNineQueries proves exactly nine queries are registered.
func TestLSQBHasNineQueries(t *testing.T) {
	wl, ok := workload.Lookup("lsqb")
	if !ok {
		t.Skip("lsqb not registered")
	}
	if len(wl.Queries) != 9 {
		t.Errorf("len(Queries)=%d, want 9", len(wl.Queries))
	}
}

// TestLSQBQueryIDs proves each query has a stable "lsqb-qN" id.
func TestLSQBQueryIDs(t *testing.T) {
	wl, ok := workload.Lookup("lsqb")
	if !ok {
		t.Skip("lsqb not registered")
	}
	seen := map[string]bool{}
	for _, q := range wl.Queries {
		if !strings.HasPrefix(q.ID, "lsqb-q") {
			t.Errorf("query %q does not have lsqb-q prefix", q.ID)
		}
		if seen[q.ID] {
			t.Errorf("duplicate query id: %s", q.ID)
		}
		seen[q.ID] = true
	}
}

// TestLSQBAllSubgraphClass proves every query is class Subgraph.
func TestLSQBAllSubgraphClass(t *testing.T) {
	wl, ok := workload.Lookup("lsqb")
	if !ok {
		t.Skip("lsqb not registered")
	}
	for _, q := range wl.Queries {
		if q.Class != target.Subgraph {
			t.Errorf("query %s: class=%v, want Subgraph", q.ID, q.Class)
		}
	}
}

// TestLSQBAllHaveCypher proves every query has a non-empty Cypher text.
func TestLSQBAllHaveCypher(t *testing.T) {
	wl, ok := workload.Lookup("lsqb")
	if !ok {
		t.Skip("lsqb not registered")
	}
	for _, q := range wl.Queries {
		text, ok := q.Texts[workload.Cypher]
		if !ok || text == "" {
			t.Errorf("query %s has no Cypher text", q.ID)
		}
	}
}

// TestLSQBAllHaveParams proves every query has a non-nil Params source.
func TestLSQBAllHaveParams(t *testing.T) {
	wl, ok := workload.Lookup("lsqb")
	if !ok {
		t.Skip("lsqb not registered")
	}
	for _, q := range wl.Queries {
		if q.Params == nil {
			t.Errorf("query %s: Params is nil", q.ID)
		}
	}
}

// TestLSQBAllHaveCountRef proves every query's compare spec has CoerceNum set.
func TestLSQBAllHaveCountRef(t *testing.T) {
	wl, ok := workload.Lookup("lsqb")
	if !ok {
		t.Skip("lsqb not registered")
	}
	for _, q := range wl.Queries {
		if !q.Reference.Compare.CoerceNum {
			t.Errorf("query %s: CoerceNum should be true (count queries must coerce)", q.ID)
		}
	}
}

// TestLSQBNoMix proves the workload has no Mix (runs in isolation only).
func TestLSQBNoMix(t *testing.T) {
	wl, ok := workload.Lookup("lsqb")
	if !ok {
		t.Skip("lsqb not registered")
	}
	if len(wl.Mix) != 0 {
		t.Errorf("LSQB should have no Mix, got %v", wl.Mix)
	}
}

// TestLSQBCypherContainsCount proves each query's Cypher contains count(*).
func TestLSQBCypherContainsCount(t *testing.T) {
	wl, ok := workload.Lookup("lsqb")
	if !ok {
		t.Skip("lsqb not registered")
	}
	for _, q := range wl.Queries {
		text := q.Texts[workload.Cypher]
		if !strings.Contains(strings.ToLower(text), "count(*)") {
			t.Errorf("query %s Cypher does not contain count(*): %s", q.ID, text)
		}
	}
}

// TestLSQBQ5CypherIsTriangle proves q5's Cypher pattern is a 3-clique (has
// three KNOWS edges forming a cycle).
func TestLSQBQ5CypherIsTriangle(t *testing.T) {
	wl, ok := workload.Lookup("lsqb")
	if !ok {
		t.Skip("lsqb not registered")
	}
	q5, ok := wl.Query("lsqb-q5")
	if !ok {
		t.Fatal("lsqb-q5 not found")
	}
	text := q5.Texts[workload.Cypher]
	// A triangle needs three persons and three KNOWS edges.
	if strings.Count(text, ":KNOWS") < 3 {
		t.Errorf("q5 should have 3 KNOWS edges (triangle), got Cypher: %s", text)
	}
	if strings.Count(text, ":Person") < 3 {
		t.Errorf("q5 should have 3 Person labels (triangle), got Cypher: %s", text)
	}
}
