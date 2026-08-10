package snb

import (
	"context"
	"math"
	"testing"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

// genSocial materializes a small social dataset in a temp dir and opens it
// back through the canonical dataset layer, the same path the runner uses.
func genSocial(t *testing.T) *dataset.Set {
	t.Helper()
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cfg := gen.Config{Kind: "social", Seed: 9, Persons: 200, AvgFriends: 8, PostsPerPerson: 4}
	if _, err := gen.Generate(context.Background(), cfg, w); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ds
}

// bind generates the dataset and binds the pools once per test.
func bind(t *testing.T) *dataset.Set {
	t.Helper()
	ds := genSocial(t)
	if err := Bind(ds); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	return ds
}

// compute runs one query's reference for one draw.
func compute(t *testing.T, ds engine.Dataset, q *workload.Query, p workload.Params) *workload.Answer {
	t.Helper()
	ans, err := q.Reference.Compute(ds, p)
	if err != nil {
		t.Fatalf("%s: reference: %v", q.ID, err)
	}
	return ans
}

// draws returns up to n pool draws for a query.
func draws(t *testing.T, q *workload.Query, n int) []workload.Params {
	t.Helper()
	pool := q.Params.Pool()
	if q.PoolKey != "" && len(pool) == 0 {
		t.Fatalf("%s: pool %q is empty after Bind", q.ID, q.PoolKey)
	}
	if len(pool) == 0 {
		return []workload.Params{q.Params.Next()}
	}
	if len(pool) > n {
		pool = pool[:n]
	}
	return pool
}

// TestRegistered checks the five workloads registered at init with the
// expected shapes (a Register panic would already have failed the import).
func TestRegistered(t *testing.T) {
	for _, tc := range []struct {
		name    string
		queries int
	}{
		{"snb-short", 7},
		{"snb-complex", 6},
		{"snb-update", 6},
		{"snb-mix", 19},
		{"snb-bi", 5},
	} {
		w, err := workload.Lookup(tc.name)
		if err != nil {
			t.Fatalf("Lookup(%s): %v", tc.name, err)
		}
		if len(w.Queries) != tc.queries {
			t.Errorf("%s: %d queries, want %d", tc.name, len(w.Queries), tc.queries)
		}
		if w.Family != "snb" || w.Fidelity != "derived" || w.Dataset != "social-1k" {
			t.Errorf("%s: family/fidelity/dataset = %q/%q/%q", tc.name, w.Family, w.Fidelity, w.Dataset)
		}
	}

	bi, _ := workload.Lookup("snb-bi")
	if !bi.Analytics {
		t.Error("snb-bi: Analytics is false, want true")
	}
	for _, name := range []string{"snb-short", "snb-complex", "snb-update"} {
		w, _ := workload.Lookup(name)
		if w.Analytics {
			t.Errorf("%s: Analytics is true, want false", name)
		}
	}
}

// TestMixWeights checks the mix sums to 100 with the v2 family split and
// that every weighted ID resolves inside the mix workload.
func TestMixWeights(t *testing.T) {
	mix, err := workload.Lookup("snb-mix")
	if err != nil {
		t.Fatal(err)
	}
	if mix.Mix == nil {
		t.Fatal("snb-mix: Mix is nil")
	}
	if mix.Mix.StreamKey != "" {
		t.Errorf("StreamKey = %q, want empty", mix.Mix.StreamKey)
	}
	var sum float64
	for id, w := range mix.Mix.Weights {
		if _, ok := mix.Query(id); !ok {
			t.Errorf("weighted query %s not in mix workload", id)
		}
		sum += w
	}
	if math.Abs(sum-100) > 1e-9 {
		t.Errorf("weights sum to %v, want 100", sum)
	}
	share := func(qs []*workload.Query) float64 {
		var s float64
		for _, q := range qs {
			s += mix.Mix.Weights[q.ID]
		}
		return s
	}
	for _, tc := range []struct {
		qs   []*workload.Query
		want float64
	}{
		{shortQueries, 72}, {complexQueries, 8}, {insertQueries, 19.8}, {deleteQueries, 0.2},
	} {
		if got := share(tc.qs); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("family share = %v, want %v", got, tc.want)
		}
	}
}

// TestWriteContract checks every update query is a stationary write: a
// post-condition, a teardown, and autocommit safety.
func TestWriteContract(t *testing.T) {
	for _, q := range updateQueries {
		if q.Class != engine.Write {
			t.Errorf("%s: class %s, want write", q.ID, q.Class)
		}
		if q.PostCondition == "" {
			t.Errorf("%s: no PostCondition", q.ID)
		}
		if q.Teardown == "" {
			t.Errorf("%s: no Teardown", q.ID)
		}
		if !q.AutocommitOK {
			t.Errorf("%s: AutocommitOK false", q.ID)
		}
		if q.Params.Next() == nil {
			t.Errorf("%s: nil params", q.ID)
		}
	}
}

// TestPools checks the inline curation produces every key with well-formed
// entries.
func TestPools(t *testing.T) {
	ds := genSocial(t)
	pools, err := Pools(ds)
	if err != nil {
		t.Fatalf("Pools: %v", err)
	}
	for key, want := range map[string][]string{
		PoolPersonID:   {"personId"},
		PoolPostID:     {"postId"},
		PoolForumID:    {"forumId"},
		PoolPersonPair: {"person1Id", "person2Id"},
		PoolPersonName: {"personId", "firstName"},
		PoolPersonDate: {"personId", "maxDate", "minDate"},
	} {
		pool := pools[key]
		if len(pool) == 0 {
			t.Errorf("pool %q is empty", key)
			continue
		}
		for _, p := range pool {
			for _, k := range want {
				if _, ok := p[k]; !ok {
					t.Errorf("pool %q entry missing %q: %v", key, k, p)
				}
			}
		}
	}
	for _, p := range pools[PoolPersonPair] {
		if p["person1Id"] == p["person2Id"] {
			t.Errorf("person-pair with identical endpoints: %v", p)
		}
	}
}

// TestShortReferences computes every snb-short reference and asserts
// non-degenerate answers.
func TestShortReferences(t *testing.T) {
	ds := bind(t)

	for _, p := range draws(t, qIS1, 8) {
		ans := compute(t, ds, qIS1, p)
		if len(ans.Rows) != 1 {
			t.Fatalf("is1(%v): %d rows, want 1", p, len(ans.Rows))
		}
		if ans.Rows[0][0].(string) == "" {
			t.Errorf("is1(%v): empty firstName", p)
		}
	}

	for _, p := range draws(t, qIS2, 8) {
		ans := compute(t, ds, qIS2, p)
		if len(ans.Rows) == 0 || len(ans.Rows) > 10 {
			t.Fatalf("is2(%v): %d rows, want 1..10", p, len(ans.Rows))
		}
		for i := 1; i < len(ans.Rows); i++ {
			if ans.Rows[i][2].(int64) > ans.Rows[i-1][2].(int64) {
				t.Errorf("is2(%v): rows not newest-first at %d", p, i)
			}
		}
	}

	for _, p := range draws(t, qIS3, 8) {
		if ans := compute(t, ds, qIS3, p); len(ans.Rows) == 0 {
			t.Errorf("is3(%v): no friends (every person has KNOWS edges)", p)
		}
	}

	for _, q := range []*workload.Query{qIS4, qIS5, qIS6} {
		for _, p := range draws(t, q, 8) {
			if ans := compute(t, ds, q, p); len(ans.Rows) != 1 {
				t.Errorf("%s(%v): %d rows, want 1", q.ID, p, len(ans.Rows))
			}
		}
	}

	var likerRows int
	for _, p := range draws(t, qIS7, 32) {
		likerRows += len(compute(t, ds, qIS7, p).Rows)
	}
	if likerRows == 0 {
		t.Error("is7: no likers across the whole post pool")
	}
}

// TestComplexReferences computes every snb-complex reference and asserts
// non-degenerate answers; ic13 must find a path for every curated pair.
func TestComplexReferences(t *testing.T) {
	ds := bind(t)

	for _, p := range draws(t, qIC1, 8) {
		if ans := compute(t, ds, qIC1, p); len(ans.Rows) == 0 {
			t.Errorf("ic1(%v): no match (pool curates a reachable firstName)", p)
		}
	}

	nonEmpty := map[string]int{}
	for _, q := range []*workload.Query{qIC2, qIC4, qIC5, qIC9} {
		for _, p := range draws(t, q, 8) {
			ans := compute(t, ds, q, p)
			if len(ans.Rows) > 20 {
				t.Errorf("%s(%v): %d rows, limit 20", q.ID, p, len(ans.Rows))
			}
			if len(ans.Rows) > 0 {
				nonEmpty[q.ID]++
			}
		}
		if nonEmpty[q.ID] == 0 {
			t.Errorf("%s: every draw returned empty", q.ID)
		}
	}

	for _, p := range draws(t, qIC13, 16) {
		ans := compute(t, ds, qIC13, p)
		if len(ans.Rows) != 1 {
			t.Fatalf("ic13(%v): %d rows, want 1 (pairs are curated connected)", p, len(ans.Rows))
		}
		if l := ans.Rows[0][0].(int64); l < 1 {
			t.Errorf("ic13(%v): path length %d, want >= 1", p, l)
		}
	}
}

// TestBIReferences computes every snb-bi reference and asserts
// non-degenerate answers; bi4 member counts must be positive.
func TestBIReferences(t *testing.T) {
	ds := bind(t)

	ans := compute(t, ds, qBI1, workload.Params{})
	if len(ans.Rows) == 0 {
		t.Fatal("bi1: no groups")
	}
	var total int64
	for _, r := range ans.Rows {
		total += r[2].(int64)
	}
	if total != 200*4 {
		t.Errorf("bi1: group counts sum to %d, want 800", total)
	}

	ans = compute(t, ds, qBI4, workload.Params{})
	if len(ans.Rows) == 0 {
		t.Fatal("bi4: no forums")
	}
	if members := ans.Rows[0][2].(int64); members <= 0 {
		t.Errorf("bi4: top forum has %d members, want > 0", members)
	}

	for _, q := range []*workload.Query{qBI5, qBI9, qBI18} {
		var nonEmpty int
		for _, p := range draws(t, q, 8) {
			if len(compute(t, ds, q, p).Rows) > 0 {
				nonEmpty++
			}
		}
		if nonEmpty == 0 {
			t.Errorf("%s: every draw returned empty", q.ID)
		}
	}
}

// TestReadReferencesPresent asserts the spec 03 §6 rule structurally: every
// non-write query in every snb workload carries a reference.
func TestReadReferencesPresent(t *testing.T) {
	for _, q := range allQueries() {
		if q.Class != engine.Write && q.Reference == nil {
			t.Errorf("%s: read query without reference", q.ID)
		}
		if _, ok := q.Texts[engine.Cypher]; !ok {
			t.Errorf("%s: no cypher text", q.ID)
		}
	}
}
