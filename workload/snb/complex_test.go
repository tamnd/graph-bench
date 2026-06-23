package snb_test

import (
	"strings"
	"testing"

	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
)

// The blank import of snb happens in snb_test.go, which is in the same package.
// Both init() calls (snb.go and complex.go) run before any test.

// TestSNBComplexRegistered proves the "snb-complex" workload is in the registry.
func TestSNBComplexRegistered(t *testing.T) {
	wl, ok := workload.Lookup("snb-complex")
	if !ok {
		t.Fatal("workload snb-complex not registered; check init() in complex.go")
	}
	if wl.Name != "snb-complex" {
		t.Errorf("Name=%q, want snb-complex", wl.Name)
	}
	if wl.Dataset == "" {
		t.Error("Dataset should not be empty")
	}
}

// TestSNBComplexHasSixQueries proves exactly six IC queries are present.
func TestSNBComplexHasSixQueries(t *testing.T) {
	wl, ok := workload.Lookup("snb-complex")
	if !ok {
		t.Skip("snb-complex not registered")
	}
	if len(wl.Queries) != 6 {
		t.Errorf("len(Queries)=%d, want 6", len(wl.Queries))
	}
}

// TestSNBComplexQueryIDs proves each query has the expected stable id.
func TestSNBComplexQueryIDs(t *testing.T) {
	wl, ok := workload.Lookup("snb-complex")
	if !ok {
		t.Skip("snb-complex not registered")
	}
	want := []string{"snb-ic1", "snb-ic2", "snb-ic6", "snb-ic8", "snb-ic9", "snb-ic11"}
	for _, id := range want {
		if _, ok := wl.Query(id); !ok {
			t.Errorf("query %s missing from snb-complex", id)
		}
	}
}

// TestSNBComplexAllTraversal proves every IC query is class Traversal.
func TestSNBComplexAllTraversal(t *testing.T) {
	wl, ok := workload.Lookup("snb-complex")
	if !ok {
		t.Skip("snb-complex not registered")
	}
	for _, q := range wl.Queries {
		if q.Class != target.Traversal {
			t.Errorf("%s: class=%v, want Traversal", q.ID, q.Class)
		}
	}
}

// TestSNBComplexAllHaveCypher proves every IC query has a non-empty Cypher text.
func TestSNBComplexAllHaveCypher(t *testing.T) {
	wl, ok := workload.Lookup("snb-complex")
	if !ok {
		t.Skip("snb-complex not registered")
	}
	for _, q := range wl.Queries {
		text, ok := q.Texts[workload.Cypher]
		if !ok || text == "" {
			t.Errorf("query %s has no Cypher text", q.ID)
		}
	}
}

// TestSNBComplexAllHaveParams proves every query has a non-nil Params source.
func TestSNBComplexAllHaveParams(t *testing.T) {
	wl, ok := workload.Lookup("snb-complex")
	if !ok {
		t.Skip("snb-complex not registered")
	}
	for _, q := range wl.Queries {
		if q.Params == nil {
			t.Errorf("query %s: Params is nil", q.ID)
		}
	}
}

// TestSNBComplexNoMix proves snb-complex has no Mix (isolation-only).
func TestSNBComplexNoMix(t *testing.T) {
	wl, ok := workload.Lookup("snb-complex")
	if !ok {
		t.Skip("snb-complex not registered")
	}
	if len(wl.Mix) != 0 {
		t.Errorf("snb-complex should have no Mix, got %v", wl.Mix)
	}
}

// TestSNBComplexOrderedResults proves IC1, IC2, IC6, IC8, IC9, IC11 all carry
// Ordered=true (their ORDER BY fully determines the result).
func TestSNBComplexOrderedResults(t *testing.T) {
	wl, ok := workload.Lookup("snb-complex")
	if !ok {
		t.Skip("snb-complex not registered")
	}
	for _, q := range wl.Queries {
		if !q.Reference.Compare.Ordered {
			t.Errorf("%s: Compare.Ordered=false, want true (has a stable ORDER BY)", q.ID)
		}
	}
}

// TestSNBComplexPersonIdParam proves all six IC queries reference $personId in
// their Cypher text.
func TestSNBComplexPersonIdParam(t *testing.T) {
	wl, ok := workload.Lookup("snb-complex")
	if !ok {
		t.Skip("snb-complex not registered")
	}
	for _, q := range wl.Queries {
		text := q.Texts[workload.Cypher]
		if !strings.Contains(text, "$personId") {
			t.Errorf("%s Cypher does not contain $personId", q.ID)
		}
	}
}

// TestSNBComplexIC1BoundedTraversal proves IC1's Cypher uses a bounded KNOWS*1..3
// pattern plus shortestPath for the distance.
func TestSNBComplexIC1BoundedTraversal(t *testing.T) {
	wl, ok := workload.Lookup("snb-complex")
	if !ok {
		t.Skip("snb-complex not registered")
	}
	q, ok := wl.Query("snb-ic1")
	if !ok {
		t.Fatal("snb-ic1 not found")
	}
	text := q.Texts[workload.Cypher]
	if !strings.Contains(text, "KNOWS*1..3") {
		t.Errorf("snb-ic1 Cypher should have KNOWS*1..3: %s", text)
	}
	if !strings.Contains(text, "shortestPath") {
		t.Errorf("snb-ic1 Cypher should use shortestPath for distance: %s", text)
	}
}

// TestSNBComplexIC6CounterCoercion proves IC6 sets CoerceNum=true (tag count
// may come back as float64 from some engines).
func TestSNBComplexIC6CounterCoercion(t *testing.T) {
	wl, ok := workload.Lookup("snb-complex")
	if !ok {
		t.Skip("snb-complex not registered")
	}
	q, ok := wl.Query("snb-ic6")
	if !ok {
		t.Fatal("snb-ic6 not found")
	}
	if !q.Reference.Compare.CoerceNum {
		t.Error("snb-ic6: CoerceNum should be true (tag count may be float64)")
	}
}

// TestSNBComplexIC9TwoHopPattern proves IC9's Cypher uses a two-hop KNOWS
// expansion for the social neighborhood.
func TestSNBComplexIC9TwoHopPattern(t *testing.T) {
	wl, ok := workload.Lookup("snb-complex")
	if !ok {
		t.Skip("snb-complex not registered")
	}
	q, ok := wl.Query("snb-ic9")
	if !ok {
		t.Fatal("snb-ic9 not found")
	}
	text := q.Texts[workload.Cypher]
	if !strings.Contains(text, "KNOWS*1..2") {
		t.Errorf("snb-ic9 Cypher should have KNOWS*1..2 for two-hop expansion: %s", text)
	}
	if !strings.Contains(text, "$maxDate") {
		t.Errorf("snb-ic9 Cypher should filter by $maxDate: %s", text)
	}
}

// TestSNBComplexIC11OrganisationJoin proves IC11's Cypher joins to Organisation
// with WORK_AT and filters by country and year.
func TestSNBComplexIC11OrganisationJoin(t *testing.T) {
	wl, ok := workload.Lookup("snb-complex")
	if !ok {
		t.Skip("snb-complex not registered")
	}
	q, ok := wl.Query("snb-ic11")
	if !ok {
		t.Fatal("snb-ic11 not found")
	}
	text := q.Texts[workload.Cypher]
	if !strings.Contains(text, "WORK_AT") {
		t.Errorf("snb-ic11 Cypher should have WORK_AT: %s", text)
	}
	if !strings.Contains(text, "$countryName") {
		t.Errorf("snb-ic11 Cypher should filter by $countryName: %s", text)
	}
	if !strings.Contains(text, "$workFromYear") {
		t.Errorf("snb-ic11 Cypher should filter by $workFromYear: %s", text)
	}
}
