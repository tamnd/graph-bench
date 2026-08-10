package lsqb_test

import (
	"context"
	"strings"
	"testing"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
	"github.com/tamnd/graph-bench/workload/lsqb"
)

// TestRegistration checks the registered shape: nine parameterless
// Aggregation count queries with Cypher texts, all with references.
func TestRegistration(t *testing.T) {
	wl, err := workload.Lookup("lsqb")
	if err != nil {
		t.Fatalf("Lookup(lsqb): %v", err)
	}
	if wl.Dataset != "social-1k" || wl.Family != "lsqb" || wl.Fidelity != "spec-following" {
		t.Errorf("dataset/family/fidelity = %q/%q/%q", wl.Dataset, wl.Family, wl.Fidelity)
	}
	if len(wl.Queries) != 9 {
		t.Fatalf("lsqb has %d queries, want 9", len(wl.Queries))
	}
	for i, q := range wl.Queries {
		wantID := []string{"lsqb-q1", "lsqb-q2", "lsqb-q3", "lsqb-q4", "lsqb-q5", "lsqb-q6", "lsqb-q7", "lsqb-q8", "lsqb-q9"}[i]
		if q.ID != wantID {
			t.Errorf("query %d id = %q, want %q", i, q.ID, wantID)
		}
		if q.Class != engine.Aggregation {
			t.Errorf("%s class = %v, want Aggregation", q.ID, q.Class)
		}
		text := q.Texts[engine.Cypher]
		if !strings.Contains(text, "count(*) AS cnt") {
			t.Errorf("%s cypher text lacks count(*) AS cnt", q.ID)
		}
		if _, ok := q.Params.(workload.Fixed); !ok {
			t.Errorf("%s params are %T, want workload.Fixed", q.ID, q.Params)
		}
	}
}

// fixtureDS hand-builds the square-with-chord fixture, small enough that
// every count is checkable on paper:
//
//	KNOWS (directed rows): P0->P1, P1->P2, P2->P3, P3->P0, P0->P2
//	  (undirected: a 4-cycle P0-P1-P2-P3 with the chord P0-P2)
//	Forum F0; posts M0(creator P0), M1(P1), M2(P2), M3(P0), all in F0
//	Members of F0: P0, P1, P2 (P3 is no member)
//	Likes: P0->M2, P1->M2, P2->M2
func fixtureDS(t *testing.T) *dataset.Set {
	t.Helper()
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	idHeader := []engine.Column{{Name: "id", Type: "ID"}}
	relHeader := []engine.Column{{Name: "", Type: "START_ID"}, {Name: "", Type: "END_ID"}, {Name: "", Type: "TYPE"}}

	writeNodes := func(label string, ids ...string) {
		f, err := w.NodeFile(label, idHeader)
		if err != nil {
			t.Fatalf("NodeFile(%s): %v", label, err)
		}
		for _, id := range ids {
			if err := f.Write([]string{id}); err != nil {
				t.Fatalf("write %s: %v", label, err)
			}
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close %s: %v", label, err)
		}
	}
	writeRels := func(typ, start, end string, pairs [][2]string) {
		f, err := w.RelFile(typ, start, end, relHeader)
		if err != nil {
			t.Fatalf("RelFile(%s): %v", typ, err)
		}
		for _, p := range pairs {
			if err := f.Write([]string{p[0], p[1], typ}); err != nil {
				t.Fatalf("write %s: %v", typ, err)
			}
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close %s: %v", typ, err)
		}
	}

	writeNodes("Person", "P0", "P1", "P2", "P3")
	writeNodes("Post", "M0", "M1", "M2", "M3")
	writeNodes("Forum", "F0")
	writeRels("KNOWS", "Person", "Person", [][2]string{{"P0", "P1"}, {"P1", "P2"}, {"P2", "P3"}, {"P3", "P0"}, {"P0", "P2"}})
	writeRels("HAS_CREATOR", "Post", "Person", [][2]string{{"M0", "P0"}, {"M1", "P1"}, {"M2", "P2"}, {"M3", "P0"}})
	writeRels("LIKES", "Person", "Post", [][2]string{{"P0", "M2"}, {"P1", "M2"}, {"P2", "M2"}})
	writeRels("HAS_MEMBER", "Forum", "Person", [][2]string{{"F0", "P0"}, {"F0", "P1"}, {"F0", "P2"}})
	writeRels("CONTAINER_OF", "Forum", "Post", [][2]string{{"F0", "M0"}, {"F0", "M1"}, {"F0", "M2"}, {"F0", "M3"}})

	if _, err := w.Finalize(&engine.Manifest{Name: "lsqb-fixture", Kind: "synthetic"}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ds
}

// TestOracleFixture pins every oracle to the hand-checked counts on the
// square-with-chord fixture. The undirected 4-cycle with a chord gives two
// triangles (q5 = 6*2) and one simple four-cycle (q7 = 8), matching v1's
// worked example.
func TestOracleFixture(t *testing.T) {
	ds := fixtureDS(t)
	want := map[string]int64{
		"lsqb-q1": 4,  // 4 containments, each creator has 1 like
		"lsqb-q2": 3,  // only M2 is liked (3x), creator P2 knows 1
		"lsqb-q3": 4,  // every contained post's creator is a member
		"lsqb-q4": 3,  // M2: one 2-chain from P2, 3 likes, 1 container
		"lsqb-q5": 12, // 2 triangles x 6
		"lsqb-q6": 12, // triangle P0P1P2 with posts 2*1*1 in F0, x6
		"lsqb-q7": 8,  // one simple 4-cycle x 8
		"lsqb-q8": 2,  // P0 has 2 posts in F0: 2 ordered pairs
		"lsqb-q9": 6,  // triangle P0P1P2 shares F0 and liked M2, x6
	}
	for id, wantN := range want {
		got, err := lsqb.CountOracle(id, ds)
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		if got != wantN {
			t.Errorf("%s = %d, want %d", id, got, wantN)
		}
	}
	if _, err := lsqb.CountOracle("lsqb-q10", ds); err == nil {
		t.Error("unknown query id: want error, got nil")
	}
}

// TestReferencesEndToEnd generates a small social graph and computes every
// registered query's reference over it, asserting the counts are all
// positive: the generated schema feeds every join shape, so a zero would
// mean a degenerate fixture or a broken oracle.
func TestReferencesEndToEnd(t *testing.T) {
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cfg := gen.Config{Kind: "social", Persons: 30, AvgFriends: 6, PostsPerPerson: 3, Seed: 7}
	if _, err := gen.Generate(context.Background(), cfg, w); err != nil {
		t.Fatalf("Generate social: %v", err)
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	wl, err := workload.Lookup("lsqb")
	if err != nil {
		t.Fatalf("Lookup(lsqb): %v", err)
	}
	for _, q := range wl.Queries {
		ans, err := q.Reference.Compute(ds, q.Params.Next())
		if err != nil {
			t.Errorf("%s reference: %v", q.ID, err)
			continue
		}
		if len(ans.Columns) != 1 || ans.Columns[0] != "cnt" || len(ans.Rows) != 1 {
			t.Errorf("%s answer shape = %v %v, want one cnt row", q.ID, ans.Columns, ans.Rows)
			continue
		}
		n, ok := ans.Rows[0][0].(int64)
		if !ok {
			t.Errorf("%s count is %T, want int64", q.ID, ans.Rows[0][0])
			continue
		}
		if n <= 0 {
			t.Errorf("%s = %d on generated social graph; want > 0", q.ID, n)
		}
		t.Logf("%s = %d", q.ID, n)
	}
}
