package snb_test

import (
	"strings"
	"testing"

	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
	_ "github.com/tamnd/graph-bench/workload/snb"
)

// TestComplexHeavyRegistered proves the snb-complex-heavy workload is registered
// with the eight deferred complex reads, all class Analytical.
func TestComplexHeavyRegistered(t *testing.T) {
	wl, ok := workload.Lookup("snb-complex-heavy")
	if !ok {
		t.Fatal("workload snb-complex-heavy not registered")
	}
	want := []string{
		"snb-ic3", "snb-ic4", "snb-ic5", "snb-ic7",
		"snb-ic10", "snb-ic12", "snb-ic13", "snb-ic14",
	}
	if len(wl.Queries) != len(want) {
		t.Errorf("len(Queries)=%d, want %d", len(wl.Queries), len(want))
	}
	for _, id := range want {
		q, ok := wl.Query(id)
		if !ok {
			t.Errorf("query %s missing", id)
			continue
		}
		if q.Class != target.Analytical {
			t.Errorf("%s class=%v, want Analytical", id, q.Class)
		}
		if text, ok := q.Texts[workload.Cypher]; !ok || text == "" {
			t.Errorf("%s has no Cypher text", id)
		}
		if q.Params == nil {
			t.Errorf("%s has nil Params", id)
		}
	}
}

// TestComplexHeavyNoOverlapWithCurated proves the heavy set and the curated set
// are disjoint: the two workloads together cover all fourteen IC reads with no id
// counted twice.
func TestComplexHeavyNoOverlapWithCurated(t *testing.T) {
	curated, _ := workload.Lookup("snb-complex")
	heavy, _ := workload.Lookup("snb-complex-heavy")
	if curated == nil || heavy == nil {
		t.Fatal("both snb-complex and snb-complex-heavy must be registered")
	}
	seen := map[string]bool{}
	for _, q := range curated.Queries {
		seen[q.ID] = true
	}
	for _, q := range heavy.Queries {
		if seen[q.ID] {
			t.Errorf("%s appears in both snb-complex and snb-complex-heavy", q.ID)
		}
	}
	if total := len(curated.Queries) + len(heavy.Queries); total != 14 {
		t.Errorf("curated+heavy cover %d IC reads, want 14", total)
	}
}

// TestComplexHeavyPathQueries proves IC13 and IC14 are the person-to-person path
// queries: IC13 uses shortestPath, IC14 uses allShortestPaths.
func TestComplexHeavyPathQueries(t *testing.T) {
	heavy, ok := workload.Lookup("snb-complex-heavy")
	if !ok {
		t.Skip("snb-complex-heavy not registered")
	}
	ic13, _ := heavy.Query("snb-ic13")
	if !strings.Contains(ic13.Texts[workload.Cypher], "shortestPath(") {
		t.Error("snb-ic13 Cypher should use shortestPath()")
	}
	ic14, _ := heavy.Query("snb-ic14")
	if !strings.Contains(ic14.Texts[workload.Cypher], "allShortestPaths(") {
		t.Error("snb-ic14 Cypher should use allShortestPaths()")
	}
}
