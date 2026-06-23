package snb_test

import (
	"strings"
	"testing"

	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
	_ "github.com/tamnd/graph-bench/workload/snb"
)

// TestSNBShortRegistered proves the "snb-short" workload is in the registry
// after the blank import.
func TestSNBShortRegistered(t *testing.T) {
	wl, ok := workload.Lookup("snb-short")
	if !ok {
		t.Fatal("workload snb-short not registered; check init() in snb.go")
	}
	if wl.Name != "snb-short" {
		t.Errorf("Name=%q, want snb-short", wl.Name)
	}
	if wl.Dataset == "" {
		t.Error("Dataset should not be empty")
	}
}

// TestSNBShortHasSevenQueries proves exactly seven IS queries are present.
func TestSNBShortHasSevenQueries(t *testing.T) {
	wl, ok := workload.Lookup("snb-short")
	if !ok {
		t.Skip("snb-short not registered")
	}
	if len(wl.Queries) != 7 {
		t.Errorf("len(Queries)=%d, want 7", len(wl.Queries))
	}
}

// TestSNBShortQueryIDs proves each query carries a stable "snb-isN" id.
func TestSNBShortQueryIDs(t *testing.T) {
	wl, ok := workload.Lookup("snb-short")
	if !ok {
		t.Skip("snb-short not registered")
	}
	want := []string{
		"snb-is1", "snb-is2", "snb-is3", "snb-is4",
		"snb-is5", "snb-is6", "snb-is7",
	}
	got := make([]string, len(wl.Queries))
	for i, q := range wl.Queries {
		got[i] = q.ID
	}
	for _, id := range want {
		found := false
		for _, g := range got {
			if g == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("query %s missing from snb-short", id)
		}
	}
}

// TestSNBShortClassAssignment proves IS1/IS4/IS5 are PointRead and IS2/IS3/IS6/IS7
// are Traversal, matching the LDBC spec classification.
func TestSNBShortClassAssignment(t *testing.T) {
	wl, ok := workload.Lookup("snb-short")
	if !ok {
		t.Skip("snb-short not registered")
	}
	pointRead := map[string]bool{"snb-is1": true, "snb-is4": true, "snb-is5": true}
	traversal := map[string]bool{"snb-is2": true, "snb-is3": true, "snb-is6": true, "snb-is7": true}
	for _, q := range wl.Queries {
		if pointRead[q.ID] && q.Class != target.PointRead {
			t.Errorf("%s: class=%v, want PointRead", q.ID, q.Class)
		}
		if traversal[q.ID] && q.Class != target.Traversal {
			t.Errorf("%s: class=%v, want Traversal", q.ID, q.Class)
		}
	}
}

// TestSNBShortAllHaveCypher proves every IS query has a non-empty Cypher text.
func TestSNBShortAllHaveCypher(t *testing.T) {
	wl, ok := workload.Lookup("snb-short")
	if !ok {
		t.Skip("snb-short not registered")
	}
	for _, q := range wl.Queries {
		text, ok := q.Texts[workload.Cypher]
		if !ok || text == "" {
			t.Errorf("query %s has no Cypher text", q.ID)
		}
	}
}

// TestSNBShortAllHaveParams proves every query has a non-nil Params source.
func TestSNBShortAllHaveParams(t *testing.T) {
	wl, ok := workload.Lookup("snb-short")
	if !ok {
		t.Skip("snb-short not registered")
	}
	for _, q := range wl.Queries {
		if q.Params == nil {
			t.Errorf("query %s: Params is nil", q.ID)
		}
	}
}

// TestSNBShortNoMix proves snb-short has no Mix (isolation-only workload).
func TestSNBShortNoMix(t *testing.T) {
	wl, ok := workload.Lookup("snb-short")
	if !ok {
		t.Skip("snb-short not registered")
	}
	if len(wl.Mix) != 0 {
		t.Errorf("snb-short should have no Mix, got %v", wl.Mix)
	}
}

// TestSNBShortPersonQueries proves IS1/IS2/IS3 Cypher texts reference $personId.
func TestSNBShortPersonQueries(t *testing.T) {
	wl, ok := workload.Lookup("snb-short")
	if !ok {
		t.Skip("snb-short not registered")
	}
	personParam := []string{"snb-is1", "snb-is2", "snb-is3"}
	for _, id := range personParam {
		q, ok := wl.Query(id)
		if !ok {
			t.Errorf("query %s not found", id)
			continue
		}
		text := q.Texts[workload.Cypher]
		if !strings.Contains(text, "$personId") {
			t.Errorf("%s Cypher does not contain $personId", id)
		}
	}
}

// TestSNBShortMessageQueries proves IS4/IS5/IS6/IS7 Cypher texts reference
// $messageId.
func TestSNBShortMessageQueries(t *testing.T) {
	wl, ok := workload.Lookup("snb-short")
	if !ok {
		t.Skip("snb-short not registered")
	}
	msgParam := []string{"snb-is4", "snb-is5", "snb-is6", "snb-is7"}
	for _, id := range msgParam {
		q, ok := wl.Query(id)
		if !ok {
			t.Errorf("query %s not found", id)
			continue
		}
		text := q.Texts[workload.Cypher]
		if !strings.Contains(text, "$messageId") {
			t.Errorf("%s Cypher does not contain $messageId", id)
		}
	}
}

// TestSNBShortOrderedQueries proves IS2, IS3, IS7 have Ordered=true (they carry
// a stable ORDER BY that fully determines the result order).
func TestSNBShortOrderedQueries(t *testing.T) {
	wl, ok := workload.Lookup("snb-short")
	if !ok {
		t.Skip("snb-short not registered")
	}
	ordered := []string{"snb-is2", "snb-is3", "snb-is7"}
	for _, id := range ordered {
		q, ok := wl.Query(id)
		if !ok {
			t.Errorf("query %s not found", id)
			continue
		}
		if !q.Reference.Compare.Ordered {
			t.Errorf("%s: Compare.Ordered=false, want true (query has a stable ORDER BY)", id)
		}
	}
}

// TestSNBShortIS6CypherHasReplyOf proves IS6 uses variable-length REPLY_OF to
// walk up to the root post.
func TestSNBShortIS6CypherHasReplyOf(t *testing.T) {
	wl, ok := workload.Lookup("snb-short")
	if !ok {
		t.Skip("snb-short not registered")
	}
	q, ok := wl.Query("snb-is6")
	if !ok {
		t.Fatal("snb-is6 not found")
	}
	text := q.Texts[workload.Cypher]
	if !strings.Contains(text, "REPLY_OF*") {
		t.Errorf("snb-is6 Cypher should have variable-length REPLY_OF*: %s", text)
	}
}

// TestSNBShortIS7CypherHasOptionalMatch proves IS7 uses OPTIONAL MATCH for the
// knows-author check.
func TestSNBShortIS7CypherHasOptionalMatch(t *testing.T) {
	wl, ok := workload.Lookup("snb-short")
	if !ok {
		t.Skip("snb-short not registered")
	}
	q, ok := wl.Query("snb-is7")
	if !ok {
		t.Fatal("snb-is7 not found")
	}
	text := q.Texts[workload.Cypher]
	if !strings.Contains(text, "OPTIONAL MATCH") {
		t.Errorf("snb-is7 Cypher should have OPTIONAL MATCH for knows check: %s", text)
	}
}
