package lsqb

import (
	"sort"
	"testing"
)

// relSet is a fixture: relationship type -> its [start, end] edges. fixtureDS
// writes one CSV per type and returns a fileDataset over them, so the oracle is
// driven through the same RelFiles/CSV path it uses in production.
type relSet map[string][][2]string

func fixtureDS(t *testing.T, rels relSet) fileDataset {
	t.Helper()
	dir := t.TempDir()
	m := map[string][]string{}
	for typ, edges := range rels {
		path := writeCSV(t, dir, typ+".csv", ":START_ID,:END_ID", edges)
		m[typ] = []string{path}
	}
	return fileDataset{rels: m}
}

// The brute-force references below count count(*) by the literal reading of
// each pattern in lsqb.go: nested loops over the actual relationship rows,
// under relationship isomorphism (relationships pairwise distinct within one
// MATCH, nodes free to coincide unless the text says otherwise). They are
// deliberately naive joins so they share no structure with the grouped,
// closed-form oracles they check: the oracle multiplies precomputed degrees,
// the brute force enumerates tuples.

// bruteQ1: (f:Forum)-[:CONTAINER_OF]->(m:Post)-[:HAS_CREATOR]->(p:Person)-[:LIKES]->(:Post)
func bruteQ1(r relSet) int64 {
	var n int64
	for _, co := range r["CONTAINER_OF"] {
		for _, hc := range r["HAS_CREATOR"] {
			if co[1] != hc[0] {
				continue
			}
			for _, lk := range r["LIKES"] {
				if hc[1] == lk[0] {
					n++
				}
			}
		}
	}
	return n
}

// bruteQ2: (m:Post)-[:HAS_CREATOR]->(p)-[:KNOWS]->(:Person), (m)<-[:LIKES]-(:Person)
func bruteQ2(r relSet) int64 {
	var n int64
	for _, hc := range r["HAS_CREATOR"] {
		for _, k := range r["KNOWS"] {
			if hc[1] != k[0] {
				continue
			}
			for _, lk := range r["LIKES"] {
				if lk[1] == hc[0] {
					n++
				}
			}
		}
	}
	return n
}

// bruteQ3: (f)-[:HAS_MEMBER]->(p), (f)-[:CONTAINER_OF]->(m)-[:HAS_CREATOR]->(p)
func bruteQ3(r relSet) int64 {
	var n int64
	for _, hm := range r["HAS_MEMBER"] {
		for _, co := range r["CONTAINER_OF"] {
			if hm[0] != co[0] {
				continue
			}
			for _, hc := range r["HAS_CREATOR"] {
				if hc[0] == co[1] && hc[1] == hm[1] {
					n++
				}
			}
		}
	}
	return n
}

// bruteQ4: (m)-[:HAS_CREATOR]->()-[:KNOWS]->()-[:KNOWS]->(), (m)<-[:LIKES]-(),
// (m)<-[:CONTAINER_OF]-(). The two KNOWS rows must be distinct relationships.
func bruteQ4(r relSet) int64 {
	knows := r["KNOWS"]
	var n int64
	for _, hc := range r["HAS_CREATOR"] {
		for i, k1 := range knows {
			if k1[0] != hc[1] {
				continue
			}
			for j, k2 := range knows {
				if i == j || k2[0] != k1[1] {
					continue
				}
				for _, lk := range r["LIKES"] {
					if lk[1] != hc[0] {
						continue
					}
					for _, co := range r["CONTAINER_OF"] {
						if co[1] == hc[0] {
							n++
						}
					}
				}
			}
		}
	}
	return n
}

// bruteQ5: the undirected KNOWS triangle, counted over ordered (a,b,c). Each
// underlying triangle contributes 6, one per permutation.
func bruteQ5(r relSet) int64 {
	adj := undirectedAdj(r["KNOWS"])
	nodes := sortedNodes(adj)
	var n int64
	for _, a := range nodes {
		for _, b := range nodes {
			if !linked(adj, a, b) {
				continue
			}
			for _, c := range nodes {
				if linked(adj, b, c) && linked(adj, c, a) {
					n++
				}
			}
		}
	}
	return n
}

// bruteQ6: the triangle joined with one post per member, all three posts in the
// same forum. ma, mb and mc carry no distinctness constraint of their own, but
// each is pinned to a different creator, so they cannot coincide.
func bruteQ6(r relSet) int64 {
	adj := undirectedAdj(r["KNOWS"])
	nodes := sortedNodes(adj)
	// posts[forum][person] is how many of that forum's posts that person wrote.
	posts := map[string]map[string]int64{}
	for _, co := range r["CONTAINER_OF"] {
		for _, hc := range r["HAS_CREATOR"] {
			if hc[0] != co[1] {
				continue
			}
			if posts[co[0]] == nil {
				posts[co[0]] = map[string]int64{}
			}
			posts[co[0]][hc[1]]++
		}
	}
	var n int64
	for _, a := range nodes {
		for _, b := range nodes {
			if !linked(adj, a, b) {
				continue
			}
			for _, c := range nodes {
				if !linked(adj, b, c) || !linked(adj, c, a) {
					continue
				}
				for _, byPerson := range posts {
					n += byPerson[a] * byPerson[b] * byPerson[c]
				}
			}
		}
	}
	return n
}

// bruteQ7: the undirected KNOWS four-cycle with the four relationships pairwise
// distinct, which the text spells out. Distinctness is checked on the
// undirected endpoint pair, since that is what identifies a KNOWS row here.
func bruteQ7(r relSet) int64 {
	adj := undirectedAdj(r["KNOWS"])
	nodes := sortedNodes(adj)
	var n int64
	for _, a := range nodes {
		for _, b := range nodes {
			if !linked(adj, a, b) {
				continue
			}
			for _, c := range nodes {
				if !linked(adj, b, c) {
					continue
				}
				for _, d := range nodes {
					if !linked(adj, c, d) || !linked(adj, d, a) {
						continue
					}
					if distinctEdges(a, b, c, d) {
						n++
					}
				}
			}
		}
	}
	return n
}

// bruteQ8: two distinct posts by one person in one forum, counted as ordered
// pairs (m1, m2).
func bruteQ8(r relSet) int64 {
	var n int64
	for _, co1 := range r["CONTAINER_OF"] {
		for _, hc1 := range r["HAS_CREATOR"] {
			if hc1[0] != co1[1] {
				continue
			}
			for _, co2 := range r["CONTAINER_OF"] {
				if co2[0] != co1[0] || co2[1] == co1[1] {
					continue
				}
				for _, hc2 := range r["HAS_CREATOR"] {
					if hc2[0] == co2[1] && hc2[1] == hc1[1] {
						n++
					}
				}
			}
		}
	}
	return n
}

// bruteQ9: the triangle whose three members share a forum and a liked post.
func bruteQ9(r relSet) int64 {
	adj := undirectedAdj(r["KNOWS"])
	nodes := sortedNodes(adj)
	members := map[string]map[string]bool{} // forum -> person
	for _, hm := range r["HAS_MEMBER"] {
		if members[hm[0]] == nil {
			members[hm[0]] = map[string]bool{}
		}
		members[hm[0]][hm[1]] = true
	}
	likers := map[string]map[string]bool{} // post -> person
	for _, lk := range r["LIKES"] {
		if likers[lk[1]] == nil {
			likers[lk[1]] = map[string]bool{}
		}
		likers[lk[1]][lk[0]] = true
	}
	shared := func(sets map[string]map[string]bool, a, b, c string) int64 {
		var k int64
		for _, s := range sets {
			if s[a] && s[b] && s[c] {
				k++
			}
		}
		return k
	}
	var n int64
	for _, a := range nodes {
		for _, b := range nodes {
			if !linked(adj, a, b) {
				continue
			}
			for _, c := range nodes {
				if !linked(adj, b, c) || !linked(adj, c, a) {
					continue
				}
				n += shared(members, a, b, c) * shared(likers, a, b, c)
			}
		}
	}
	return n
}

func linked(adj map[string]map[string]struct{}, a, b string) bool {
	_, ok := adj[a][b]
	return ok
}

// distinctEdges reports whether the four undirected hops of the cycle
// a-b-c-d-a are four different relationships.
func distinctEdges(a, b, c, d string) bool {
	hops := [4][2]string{{a, b}, {b, c}, {c, d}, {d, a}}
	seen := map[[2]string]bool{}
	for _, h := range hops {
		if h[0] > h[1] {
			h[0], h[1] = h[1], h[0]
		}
		if seen[h] {
			return false
		}
		seen[h] = true
	}
	return true
}

func sortedNodes(adj map[string]map[string]struct{}) []string {
	out := make([]string, 0, len(adj))
	for n := range adj {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// TestOraclesAgainstBruteForce checks every closed-form oracle against a naive
// enumeration of the same pattern. The hand-computed expectations in
// lsqb_test.go pin one fixture; this pins the algorithm, on fixtures shaped to
// break the shortcuts a grouped count can take: an edge that must not join, a
// hub that fans out, a duplicate that has to be counted with multiplicity, and
// cycles that share edges.
func TestOraclesAgainstBruteForce(t *testing.T) {
	cases := []struct {
		name  string
		query string
		brute func(relSet) int64
		rels  relSet
	}{
		{
			name:  "q1 chain with a creator who likes twice",
			query: "lsqb-q1",
			brute: bruteQ1,
			rels: relSet{
				"CONTAINER_OF": {{"f1", "m1"}, {"f1", "m2"}, {"f2", "m3"}},
				"HAS_CREATOR":  {{"m1", "p1"}, {"m2", "p1"}, {"m3", "p2"}},
				"LIKES":        {{"p1", "m3"}, {"p1", "m2"}, {"p2", "m1"}},
			},
		},
		{
			name:  "q1 post with no container contributes nothing",
			query: "lsqb-q1",
			brute: bruteQ1,
			rels: relSet{
				"CONTAINER_OF": {{"f1", "m1"}},
				"HAS_CREATOR":  {{"m1", "p1"}, {"m9", "p9"}},
				"LIKES":        {{"p1", "m1"}, {"p9", "m1"}},
			},
		},
		{
			name:  "q2 hub post liked three times by a well-connected creator",
			query: "lsqb-q2",
			brute: bruteQ2,
			rels: relSet{
				"HAS_CREATOR": {{"m1", "p1"}, {"m2", "p2"}},
				"KNOWS":       {{"p1", "p2"}, {"p1", "p3"}, {"p2", "p1"}},
				"LIKES":       {{"p2", "m1"}, {"p3", "m1"}, {"p4", "m1"}},
			},
		},
		{
			name:  "q2 creator with no friends drops out",
			query: "lsqb-q2",
			brute: bruteQ2,
			rels: relSet{
				"HAS_CREATOR": {{"m1", "p1"}},
				"KNOWS":       {{"p2", "p3"}},
				"LIKES":       {{"p2", "m1"}},
			},
		},
		{
			name:  "q3 member who is also the creator, and one who is not",
			query: "lsqb-q3",
			brute: bruteQ3,
			rels: relSet{
				"HAS_MEMBER":   {{"f1", "p1"}, {"f1", "p2"}, {"f2", "p1"}},
				"CONTAINER_OF": {{"f1", "m1"}, {"f1", "m2"}, {"f2", "m3"}},
				"HAS_CREATOR":  {{"m1", "p1"}, {"m2", "p2"}, {"m3", "p2"}},
			},
		},
		{
			name:  "q3 creator in the wrong forum does not close the diamond",
			query: "lsqb-q3",
			brute: bruteQ3,
			rels: relSet{
				"HAS_MEMBER":   {{"f1", "p1"}},
				"CONTAINER_OF": {{"f2", "m1"}},
				"HAS_CREATOR":  {{"m1", "p1"}},
			},
		},
		{
			name:  "q4 two-hop friend chain with a reciprocal edge",
			query: "lsqb-q4",
			brute: bruteQ4,
			rels: relSet{
				"HAS_CREATOR":  {{"m1", "p1"}},
				"KNOWS":        {{"p1", "p2"}, {"p2", "p3"}, {"p2", "p1"}, {"p3", "p4"}},
				"LIKES":        {{"p5", "m1"}, {"p6", "m1"}},
				"CONTAINER_OF": {{"f1", "m1"}, {"f2", "m1"}},
			},
		},
		{
			name:  "q4 one-hop only, no chain to extend",
			query: "lsqb-q4",
			brute: bruteQ4,
			rels: relSet{
				"HAS_CREATOR":  {{"m1", "p1"}},
				"KNOWS":        {{"p1", "p2"}},
				"LIKES":        {{"p3", "m1"}},
				"CONTAINER_OF": {{"f1", "m1"}},
			},
		},
		{
			name:  "q5 two triangles sharing an edge",
			query: "lsqb-q5",
			brute: bruteQ5,
			rels: relSet{
				"KNOWS": {{"a", "b"}, {"b", "c"}, {"c", "a"}, {"c", "d"}, {"d", "a"}},
			},
		},
		{
			name:  "q5 four-cycle has no triangle",
			query: "lsqb-q5",
			brute: bruteQ5,
			rels: relSet{
				"KNOWS": {{"a", "b"}, {"b", "c"}, {"c", "d"}, {"d", "a"}},
			},
		},
		{
			name:  "q6 triangle whose members post in two shared forums",
			query: "lsqb-q6",
			brute: bruteQ6,
			rels: relSet{
				"KNOWS":        {{"a", "b"}, {"b", "c"}, {"c", "a"}},
				"CONTAINER_OF": {{"f1", "ma"}, {"f1", "mb"}, {"f1", "mc"}, {"f2", "na"}, {"f2", "nb"}, {"f2", "nc"}},
				"HAS_CREATOR":  {{"ma", "a"}, {"mb", "b"}, {"mc", "c"}, {"na", "a"}, {"nb", "b"}, {"nc", "c"}},
			},
		},
		{
			name:  "q6 one member posts twice in the forum",
			query: "lsqb-q6",
			brute: bruteQ6,
			rels: relSet{
				"KNOWS":        {{"a", "b"}, {"b", "c"}, {"c", "a"}},
				"CONTAINER_OF": {{"f1", "ma"}, {"f1", "ma2"}, {"f1", "mb"}, {"f1", "mc"}},
				"HAS_CREATOR":  {{"ma", "a"}, {"ma2", "a"}, {"mb", "b"}, {"mc", "c"}},
			},
		},
		{
			name:  "q6 posts split across forums never share one",
			query: "lsqb-q6",
			brute: bruteQ6,
			rels: relSet{
				"KNOWS":        {{"a", "b"}, {"b", "c"}, {"c", "a"}},
				"CONTAINER_OF": {{"f1", "ma"}, {"f1", "mb"}, {"f2", "mc"}},
				"HAS_CREATOR":  {{"ma", "a"}, {"mb", "b"}, {"mc", "c"}},
			},
		},
		{
			name:  "q7 one simple four-cycle",
			query: "lsqb-q7",
			brute: bruteQ7,
			rels: relSet{
				"KNOWS": {{"a", "b"}, {"b", "c"}, {"c", "d"}, {"d", "a"}},
			},
		},
		{
			name:  "q7 square with a chord",
			query: "lsqb-q7",
			brute: bruteQ7,
			rels: relSet{
				"KNOWS": {{"a", "b"}, {"b", "c"}, {"c", "d"}, {"d", "a"}, {"a", "c"}},
			},
		},
		{
			name:  "q7 complete K4",
			query: "lsqb-q7",
			brute: bruteQ7,
			rels: relSet{
				"KNOWS": {{"a", "b"}, {"a", "c"}, {"a", "d"}, {"b", "c"}, {"b", "d"}, {"c", "d"}},
			},
		},
		{
			name:  "q7 triangle admits no four-cycle",
			query: "lsqb-q7",
			brute: bruteQ7,
			rels: relSet{
				"KNOWS": {{"a", "b"}, {"b", "c"}, {"c", "a"}},
			},
		},
		{
			name:  "q8 three posts by one person in one forum",
			query: "lsqb-q8",
			brute: bruteQ8,
			rels: relSet{
				"CONTAINER_OF": {{"f1", "m1"}, {"f1", "m2"}, {"f1", "m3"}},
				"HAS_CREATOR":  {{"m1", "p1"}, {"m2", "p1"}, {"m3", "p1"}},
			},
		},
		{
			name:  "q8 same person, different forums, cannot pair",
			query: "lsqb-q8",
			brute: bruteQ8,
			rels: relSet{
				"CONTAINER_OF": {{"f1", "m1"}, {"f2", "m2"}},
				"HAS_CREATOR":  {{"m1", "p1"}, {"m2", "p1"}},
			},
		},
		{
			name:  "q8 same forum, different people, cannot pair",
			query: "lsqb-q8",
			brute: bruteQ8,
			rels: relSet{
				"CONTAINER_OF": {{"f1", "m1"}, {"f1", "m2"}},
				"HAS_CREATOR":  {{"m1", "p1"}, {"m2", "p2"}},
			},
		},
		{
			name:  "q9 triangle sharing two forums and two liked posts",
			query: "lsqb-q9",
			brute: bruteQ9,
			rels: relSet{
				"KNOWS":      {{"a", "b"}, {"b", "c"}, {"c", "a"}},
				"HAS_MEMBER": {{"f1", "a"}, {"f1", "b"}, {"f1", "c"}, {"f2", "a"}, {"f2", "b"}, {"f2", "c"}},
				"LIKES":      {{"a", "m1"}, {"b", "m1"}, {"c", "m1"}, {"a", "m2"}, {"b", "m2"}, {"c", "m2"}},
			},
		},
		{
			name:  "q9 one member missing from the forum",
			query: "lsqb-q9",
			brute: bruteQ9,
			rels: relSet{
				"KNOWS":      {{"a", "b"}, {"b", "c"}, {"c", "a"}},
				"HAS_MEMBER": {{"f1", "a"}, {"f1", "b"}},
				"LIKES":      {{"a", "m1"}, {"b", "m1"}, {"c", "m1"}},
			},
		},
		{
			name:  "q9 shared forum but no post all three liked",
			query: "lsqb-q9",
			brute: bruteQ9,
			rels: relSet{
				"KNOWS":      {{"a", "b"}, {"b", "c"}, {"c", "a"}},
				"HAS_MEMBER": {{"f1", "a"}, {"f1", "b"}, {"f1", "c"}},
				"LIKES":      {{"a", "m1"}, {"b", "m1"}, {"c", "m2"}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ds := fixtureDS(t, tc.rels)
			got, err := CountOracle(tc.query, ds)
			if err != nil {
				t.Fatalf("%s: %v", tc.query, err)
			}
			want := tc.brute(tc.rels)
			if got != want {
				t.Errorf("%s: CountOracle=%d, brute force=%d", tc.query, got, want)
			}
		})
	}
}
