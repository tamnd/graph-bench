package snb_test

import (
	"strings"
	"testing"

	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
)

// The blank import of snb happens in snb_test.go.

// TestSNBWriteRegistered proves the "snb-write" workload is in the registry.
func TestSNBWriteRegistered(t *testing.T) {
	wl, ok := workload.Lookup("snb-write")
	if !ok {
		t.Fatal("workload snb-write not registered; check init() in writes.go")
	}
	if wl.Name != "snb-write" {
		t.Errorf("Name=%q, want snb-write", wl.Name)
	}
}

// TestSNBWriteHasSixteenQueries proves exactly 8 inserts + 8 deletes = 16 write
// operations are registered.
func TestSNBWriteHasSixteenQueries(t *testing.T) {
	wl, ok := workload.Lookup("snb-write")
	if !ok {
		t.Skip("snb-write not registered")
	}
	if len(wl.Queries) != 16 {
		t.Errorf("len(Queries)=%d, want 16 (8 inserts + 8 deletes)", len(wl.Queries))
	}
}

// TestSNBWriteQueryIDs proves all 16 write operation ids are present.
func TestSNBWriteQueryIDs(t *testing.T) {
	wl, ok := workload.Lookup("snb-write")
	if !ok {
		t.Skip("snb-write not registered")
	}
	want := []string{
		"snb-iu1", "snb-iu2", "snb-iu3", "snb-iu4",
		"snb-iu5", "snb-iu6", "snb-iu7", "snb-iu8",
		"snb-id1", "snb-id2", "snb-id3", "snb-id4",
		"snb-id5", "snb-id6", "snb-id7", "snb-id8",
	}
	for _, id := range want {
		if _, ok := wl.Query(id); !ok {
			t.Errorf("query %s missing from snb-write", id)
		}
	}
}

// TestSNBWriteAllWriteClass proves every write operation is class Write.
func TestSNBWriteAllWriteClass(t *testing.T) {
	wl, ok := workload.Lookup("snb-write")
	if !ok {
		t.Skip("snb-write not registered")
	}
	for _, q := range wl.Queries {
		if q.Class != target.Write {
			t.Errorf("%s: class=%v, want Write", q.ID, q.Class)
		}
	}
}

// TestSNBWriteAllHaveCypher proves every write operation has Cypher text.
func TestSNBWriteAllHaveCypher(t *testing.T) {
	wl, ok := workload.Lookup("snb-write")
	if !ok {
		t.Skip("snb-write not registered")
	}
	for _, q := range wl.Queries {
		text, ok := q.Texts[workload.Cypher]
		if !ok || text == "" {
			t.Errorf("query %s has no Cypher text", q.ID)
		}
	}
}

// TestSNBWriteAllHaveParams proves every write operation has a non-nil param source.
func TestSNBWriteAllHaveParams(t *testing.T) {
	wl, ok := workload.Lookup("snb-write")
	if !ok {
		t.Skip("snb-write not registered")
	}
	for _, q := range wl.Queries {
		if q.Params == nil {
			t.Errorf("query %s: Params is nil", q.ID)
		}
	}
}

// TestSNBWriteNoMix proves snb-write has no Mix (writes run in isolation too).
func TestSNBWriteNoMix(t *testing.T) {
	wl, ok := workload.Lookup("snb-write")
	if !ok {
		t.Skip("snb-write not registered")
	}
	if len(wl.Mix) != 0 {
		t.Errorf("snb-write should have no Mix, got %d entries", len(wl.Mix))
	}
}

// TestSNBWriteIU1CreatesPersonNode proves IU1 Cypher creates a Person node.
func TestSNBWriteIU1CreatesPersonNode(t *testing.T) {
	wl, ok := workload.Lookup("snb-write")
	if !ok {
		t.Skip("snb-write not registered")
	}
	q, ok := wl.Query("snb-iu1")
	if !ok {
		t.Fatal("snb-iu1 not found")
	}
	text := q.Texts[workload.Cypher]
	if !strings.Contains(text, "CREATE (p:Person") {
		t.Errorf("snb-iu1 should CREATE a Person node: %s", text)
	}
	if !strings.Contains(text, "IS_LOCATED_IN") {
		t.Errorf("snb-iu1 should set IS_LOCATED_IN: %s", text)
	}
}

// TestSNBWriteIU6CreatesFriendship proves IU6 creates a KNOWS edge.
func TestSNBWriteIU6CreatesFriendship(t *testing.T) {
	wl, ok := workload.Lookup("snb-write")
	if !ok {
		t.Skip("snb-write not registered")
	}
	q, ok := wl.Query("snb-iu6")
	if !ok {
		t.Fatal("snb-iu6 not found")
	}
	text := q.Texts[workload.Cypher]
	if !strings.Contains(text, "CREATE") || !strings.Contains(text, ":KNOWS") {
		t.Errorf("snb-iu6 should CREATE a KNOWS edge: %s", text)
	}
}

// TestSNBWriteID1CascadesDelete proves ID1 uses DETACH DELETE for both messages
// and the person (the v2 deep-delete pattern).
func TestSNBWriteID1CascadesDelete(t *testing.T) {
	wl, ok := workload.Lookup("snb-write")
	if !ok {
		t.Skip("snb-write not registered")
	}
	q, ok := wl.Query("snb-id1")
	if !ok {
		t.Fatal("snb-id1 not found")
	}
	text := q.Texts[workload.Cypher]
	if strings.Count(text, "DETACH DELETE") < 2 {
		t.Errorf("snb-id1 should have 2 DETACH DELETE clauses (messages then person): %s", text)
	}
}

// TestSNBMixRegistered proves the "snb-mix" workload is in the registry.
func TestSNBMixRegistered(t *testing.T) {
	wl, ok := workload.Lookup("snb-mix")
	if !ok {
		t.Fatal("workload snb-mix not registered; check init() in mix.go")
	}
	if wl.Name != "snb-mix" {
		t.Errorf("Name=%q, want snb-mix", wl.Name)
	}
}

// TestSNBMixHasCorrectQueryCount proves snb-mix has 7+6+16=29 queries.
func TestSNBMixHasCorrectQueryCount(t *testing.T) {
	wl, ok := workload.Lookup("snb-mix")
	if !ok {
		t.Skip("snb-mix not registered")
	}
	// 7 short + 6 complex + 8 inserts + 8 deletes = 29
	if len(wl.Queries) != 29 {
		t.Errorf("len(Queries)=%d, want 29 (7+6+8+8)", len(wl.Queries))
	}
}

// TestSNBMixHasMixWeights proves the Mix map has an entry for every query.
func TestSNBMixHasMixWeights(t *testing.T) {
	wl, ok := workload.Lookup("snb-mix")
	if !ok {
		t.Skip("snb-mix not registered")
	}
	for _, q := range wl.Queries {
		w, ok := wl.Mix[q.ID]
		if !ok {
			t.Errorf("snb-mix: no Mix weight for query %s", q.ID)
		} else if w <= 0 {
			t.Errorf("snb-mix: weight for %s is %f, want > 0", q.ID, w)
		}
	}
}

// TestSNBMixShortReadsFrequencyRatio proves IS1-IS5 fire at twice the rate of
// IS6-IS7 (the LDBC frequency table 2:1 ratio).
func TestSNBMixShortReadsFrequencyRatio(t *testing.T) {
	wl, ok := workload.Lookup("snb-mix")
	if !ok {
		t.Skip("snb-mix not registered")
	}
	highFreq := []string{"snb-is1", "snb-is2", "snb-is3", "snb-is4", "snb-is5"}
	lowFreq := []string{"snb-is6", "snb-is7"}
	for _, hi := range highFreq {
		for _, lo := range lowFreq {
			if wl.Mix[hi] != 2*wl.Mix[lo] {
				t.Errorf("Mix[%s]=%f should be 2*Mix[%s]=%f", hi, wl.Mix[hi], lo, wl.Mix[lo])
			}
		}
	}
}

// TestSNBMixGroupProportions proves the sum of weights within each group matches
// the LDBC v2 proportions: 72 short, 8 complex, 20 insert, 0.2 delete.
func TestSNBMixGroupProportions(t *testing.T) {
	wl, ok := workload.Lookup("snb-mix")
	if !ok {
		t.Skip("snb-mix not registered")
	}
	var shortSum, complexSum, insertSum, deleteSum float64
	for id, w := range wl.Mix {
		switch {
		case strings.HasPrefix(id, "snb-is"):
			shortSum += w
		case strings.HasPrefix(id, "snb-ic"):
			complexSum += w
		case strings.HasPrefix(id, "snb-iu"):
			insertSum += w
		case strings.HasPrefix(id, "snb-id"):
			deleteSum += w
		}
	}
	const tol = 1e-9
	if abs(shortSum-72.0) > tol {
		t.Errorf("short read weight sum=%.4f, want 72.0", shortSum)
	}
	if abs(complexSum-8.0) > tol {
		t.Errorf("complex read weight sum=%.6f, want 8.0", complexSum)
	}
	if abs(insertSum-20.0) > tol {
		t.Errorf("insert weight sum=%.4f, want 20.0", insertSum)
	}
	if abs(deleteSum-0.2) > tol {
		t.Errorf("delete weight sum=%.4f, want 0.2", deleteSum)
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
