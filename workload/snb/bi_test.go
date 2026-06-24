package snb_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
	_ "github.com/tamnd/graph-bench/workload/snb"
)

// TestBIRegistered proves the snb-bi workload registers all twenty BI reads
// bi1-bi20, each class Analytical with Cypher text and a param source.
func TestBIRegistered(t *testing.T) {
	wl, ok := workload.Lookup("snb-bi")
	if !ok {
		t.Fatal("workload snb-bi not registered")
	}
	if len(wl.Queries) != 20 {
		t.Errorf("len(Queries)=%d, want 20", len(wl.Queries))
	}
	for i := 1; i <= 20; i++ {
		id := fmt.Sprintf("snb-bi%d", i)
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

// TestBINoOverlapWithComplex proves the BI ids do not collide with the IC reads.
func TestBINoOverlapWithComplex(t *testing.T) {
	bi, _ := workload.Lookup("snb-bi")
	if bi == nil {
		t.Fatal("snb-bi must be registered")
	}
	for _, q := range bi.Queries {
		if !strings.HasPrefix(q.ID, "snb-bi") {
			t.Errorf("unexpected id %s in snb-bi", q.ID)
		}
	}
}

// TestBIPathQueries proves the three weighted-path queries carry a path pattern:
// BI15 and BI19 use shortestPath, BI20 uses shortestPath from company employees.
func TestBIPathQueries(t *testing.T) {
	bi, ok := workload.Lookup("snb-bi")
	if !ok {
		t.Skip("snb-bi not registered")
	}
	for _, id := range []string{"snb-bi15", "snb-bi19", "snb-bi20"} {
		q, _ := bi.Query(id)
		if q == nil {
			t.Errorf("%s missing", id)
			continue
		}
		if !strings.Contains(q.Texts[workload.Cypher], "shortestPath(") {
			t.Errorf("%s should carry a shortestPath pattern", id)
		}
	}
}

// TestBITriangleQuery proves BI11 is the cyclic friend-triangle archetype: three
// KNOWS edges closing a cycle with an id ordering to dedupe.
func TestBITriangleQuery(t *testing.T) {
	bi, ok := workload.Lookup("snb-bi")
	if !ok {
		t.Skip("snb-bi not registered")
	}
	q, _ := bi.Query("snb-bi11")
	if q == nil {
		t.Fatal("snb-bi11 missing")
	}
	cy := q.Texts[workload.Cypher]
	if strings.Count(cy, ":KNOWS]") != 3 {
		t.Errorf("snb-bi11 should close a triangle with three KNOWS edges")
	}
	if !strings.Contains(cy, "count(*)") {
		t.Error("snb-bi11 should return count(*)")
	}
}
