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

// The brute-force references below count count(*) directly by the literal
// reading of each pattern: a nested loop over the actual relationship rows under
// relationship-isomorphism (relationships pairwise distinct, nodes may coincide).
// They are deliberately naive joins so they share no structure with the indexed
// closed-form oracles they check.

func bruteQ1(r relSet) int64 {
	var n int64
	for _, loc := range r["IS_LOCATED_IN"] { // (p)-loc->(city)
		p, city := loc[0], loc[1]
		for _, po := range r["IS_PART_OF"] { // (city)-partOf->(country)
			if po[0] != city {
				continue
			}
			for _, sa := range r["STUDY_AT"] { // (p)-studyAt->(univ)
				if sa[0] == p {
					n++
				}
			}
		}
	}
	return n
}

func bruteQ2(r relSet) int64 {
	var n int64
	for _, hc := range r["HAS_CREATOR"] { // (m)-creator->(p)
		m, p := hc[0], hc[1]
		for _, loc := range r["IS_LOCATED_IN"] { // (p)-loc->(city)
			if loc[0] != p {
				continue
			}
			for _, ht := range r["HAS_TAG"] { // (m)-tag->(t)
				if ht[0] == m {
					n++
				}
			}
		}
	}
	return n
}

func bruteQ3(r relSet) int64 {
	var n int64
	for _, mod := range r["HAS_MODERATOR"] { // (f)-moderator->(w)
		f := mod[0]
		for _, mem := range r["HAS_MEMBER"] { // (f)-member->(p)
			if mem[0] != f {
				continue
			}
			p := mem[1]
			for _, co := range r["CONTAINER_OF"] { // (f)-contains->(m)
				if co[0] != f {
					continue
				}
				m := co[1]
				for _, hc := range r["HAS_CREATOR"] { // (m)-creator->(p)
					if hc[0] == m && hc[1] == p {
						n++
					}
				}
			}
		}
	}
	return n
}

func bruteQ4(r relSet) int64 {
	var n int64
	for _, ht := range r["HAS_TAG"] { // (m)-tag->(t)
		m, tag := ht[0], ht[1]
		for _, hy := range r["HAS_TYPE"] { // (t)-type->(class)
			if hy[0] != tag {
				continue
			}
			for _, hc := range r["HAS_CREATOR"] { // (m)-creator->(p)
				if hc[0] != m {
					continue
				}
				p := hc[1]
				for _, loc := range r["IS_LOCATED_IN"] { // (p)-loc->(city)
					if loc[0] != p {
						continue
					}
					city := loc[1]
					for _, po := range r["IS_PART_OF"] { // (city)-partOf->(country)
						if po[0] == city {
							n++
						}
					}
				}
			}
		}
	}
	return n
}

// messageTagCount[(person,tag)] = number of messages that person created
// carrying that tag, built literally from HAS_CREATOR and HAS_TAG. Shared by the
// Q6 and Q9 brute forces.
func messageTagCount(r relSet) map[[2]string]int64 {
	creator := map[string]string{}
	for _, e := range r["HAS_CREATOR"] {
		creator[e[0]] = e[1]
	}
	cnt := map[[2]string]int64{}
	for _, ht := range r["HAS_TAG"] {
		if p, ok := creator[ht[0]]; ok {
			cnt[[2]string{p, ht[1]}]++
		}
	}
	return cnt
}

func tagUniverse(r relSet) []string {
	seen := map[string]struct{}{}
	for _, ht := range r["HAS_TAG"] {
		seen[ht[1]] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// bruteQ6 enumerates every ordered distinct triple (a,b,c) forming a KNOWS
// triangle, and for each shared tag multiplies the three message counts. The
// ordered enumeration visits each distinct triangle six times, so it returns
// count(*) directly.
func bruteQ6(r relSet) int64 {
	adj := undirectedAdj(r["KNOWS"])
	cnt := messageTagCount(r)
	tags := tagUniverse(r)
	nodes := sortedNodes(adj)
	edge := func(a, b string) bool { _, ok := adj[a][b]; return ok }
	var total int64
	for _, a := range nodes {
		for _, b := range nodes {
			for _, c := range nodes {
				if a == b || b == c || a == c {
					continue
				}
				if !edge(a, b) || !edge(b, c) || !edge(c, a) {
					continue
				}
				for _, t := range tags {
					total += cnt[[2]string{a, t}] * cnt[[2]string{b, t}] * cnt[[2]string{c, t}]
				}
			}
		}
	}
	return total
}

// bruteQ9 extends bruteQ6 with a shared forum: for each ordered triangle it
// multiplies the shared-tag sum by the number of forums having all three as
// members.
func bruteQ9(r relSet) int64 {
	adj := undirectedAdj(r["KNOWS"])
	cnt := messageTagCount(r)
	tags := tagUniverse(r)
	nodes := sortedNodes(adj)
	edge := func(a, b string) bool { _, ok := adj[a][b]; return ok }

	memberForums := map[string]map[string]struct{}{}
	for _, e := range r["HAS_MEMBER"] {
		f, p := e[0], e[1]
		if memberForums[p] == nil {
			memberForums[p] = map[string]struct{}{}
		}
		memberForums[p][f] = struct{}{}
	}
	common := func(a, b, c string) int64 {
		var n int64
		for f := range memberForums[a] {
			if _, ok := memberForums[b][f]; !ok {
				continue
			}
			if _, ok := memberForums[c][f]; !ok {
				continue
			}
			n++
		}
		return n
	}

	var total int64
	for _, a := range nodes {
		for _, b := range nodes {
			for _, c := range nodes {
				if a == b || b == c || a == c {
					continue
				}
				if !edge(a, b) || !edge(b, c) || !edge(c, a) {
					continue
				}
				var tagSum int64
				for _, t := range tags {
					tagSum += cnt[[2]string{a, t}] * cnt[[2]string{b, t}] * cnt[[2]string{c, t}]
				}
				total += common(a, b, c) * tagSum
			}
		}
	}
	return total
}

func sortedNodes(adj map[string]map[string]struct{}) []string {
	out := make([]string, 0, len(adj))
	for n := range adj {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// TestTreeAndCompositeOraclesAgainstBruteForce drives each new oracle through
// the CSV path and compares it to the literal-join brute force on a range of
// fixtures, including ones with spurious cross-label edges that the label-pinning
// join must exclude.
func TestTreeAndCompositeOraclesAgainstBruteForce(t *testing.T) {
	cases := []struct {
		name  string
		query string
		brute func(relSet) int64
		rels  relSet
	}{
		{
			name:  "q1 single chain",
			query: "lsqb-q1",
			brute: bruteQ1,
			rels: relSet{
				"IS_LOCATED_IN": {{"p1", "city1"}},
				"IS_PART_OF":    {{"city1", "country1"}},
				"STUDY_AT":      {{"p1", "uni1"}},
			},
		},
		{
			name:  "q1 fan-out: two universities, two countries for the city",
			query: "lsqb-q1",
			brute: bruteQ1,
			rels: relSet{
				"IS_LOCATED_IN": {{"p1", "city1"}},
				"IS_PART_OF":    {{"city1", "country1"}, {"city1", "country2"}},
				"STUDY_AT":      {{"p1", "uni1"}, {"p1", "uni2"}},
			},
		},
		{
			name:  "q1 spurious message located-in country is excluded",
			query: "lsqb-q1",
			brute: bruteQ1,
			rels: relSet{
				// m9 is a Message located in a Country; it must not act as a person.
				"IS_LOCATED_IN": {{"p1", "city1"}, {"m9", "country1"}},
				"IS_PART_OF":    {{"city1", "country1"}},
				"STUDY_AT":      {{"p1", "uni1"}},
			},
		},
		{
			name:  "q2 message with two tags, creator in one city",
			query: "lsqb-q2",
			brute: bruteQ2,
			rels: relSet{
				"HAS_CREATOR":   {{"m1", "p1"}},
				"IS_LOCATED_IN": {{"p1", "city1"}},
				"HAS_TAG":       {{"m1", "t1"}, {"m1", "t2"}},
			},
		},
		{
			name:  "q2 spurious forum tag is excluded",
			query: "lsqb-q2",
			brute: bruteQ2,
			rels: relSet{
				"HAS_CREATOR":   {{"m1", "p1"}},
				"IS_LOCATED_IN": {{"p1", "city1"}},
				// f9 is a Forum tagged t3; f9 is not a creator so it cannot match.
				"HAS_TAG": {{"m1", "t1"}, {"f9", "t3"}},
			},
		},
		{
			name:  "q3 moderator may coincide with the member-creator",
			query: "lsqb-q3",
			brute: bruteQ3,
			rels: relSet{
				"HAS_MODERATOR": {{"f1", "p1"}},
				"HAS_MEMBER":    {{"f1", "p1"}, {"f1", "p2"}},
				"CONTAINER_OF":  {{"f1", "m1"}, {"f1", "m2"}},
				"HAS_CREATOR":   {{"m1", "p1"}, {"m2", "p3"}}, // m2's creator p3 is not a member
			},
		},
		{
			name:  "q3 two moderators scale the inner count",
			query: "lsqb-q3",
			brute: bruteQ3,
			rels: relSet{
				"HAS_MODERATOR": {{"f1", "w1"}, {"f1", "w2"}},
				"HAS_MEMBER":    {{"f1", "p1"}, {"f1", "p2"}},
				"CONTAINER_OF":  {{"f1", "m1"}, {"f1", "m2"}},
				"HAS_CREATOR":   {{"m1", "p1"}, {"m2", "p2"}},
			},
		},
		{
			name:  "q4 two tags and a two-country location chain",
			query: "lsqb-q4",
			brute: bruteQ4,
			rels: relSet{
				"HAS_TAG":       {{"m1", "t1"}, {"m1", "t2"}},
				"HAS_TYPE":      {{"t1", "tc1"}, {"t2", "tc1"}},
				"HAS_CREATOR":   {{"m1", "p1"}},
				"IS_LOCATED_IN": {{"p1", "city1"}},
				"IS_PART_OF":    {{"city1", "country1"}},
			},
		},
		{
			name:  "q6 triangle, two share a tag, all three share another",
			query: "lsqb-q6",
			brute: bruteQ6,
			rels: relSet{
				"KNOWS":       {{"a", "b"}, {"b", "c"}, {"c", "a"}},
				"HAS_CREATOR": {{"ma", "a"}, {"mb", "b"}, {"mc", "c"}, {"ma2", "a"}},
				"HAS_TAG":     {{"ma", "t1"}, {"mb", "t1"}, {"mc", "t1"}, {"ma2", "t1"}},
			},
		},
		{
			name:  "q6 no shared tag across the triangle",
			query: "lsqb-q6",
			brute: bruteQ6,
			rels: relSet{
				"KNOWS":       {{"a", "b"}, {"b", "c"}, {"c", "a"}},
				"HAS_CREATOR": {{"ma", "a"}, {"mb", "b"}, {"mc", "c"}},
				"HAS_TAG":     {{"ma", "t1"}, {"mb", "t1"}, {"mc", "t2"}},
			},
		},
		{
			name:  "q6 two triangles sharing an edge",
			query: "lsqb-q6",
			brute: bruteQ6,
			rels: relSet{
				"KNOWS":       {{"a", "b"}, {"b", "c"}, {"c", "a"}, {"a", "d"}, {"b", "d"}},
				"HAS_CREATOR": {{"ma", "a"}, {"mb", "b"}, {"mc", "c"}, {"md", "d"}},
				"HAS_TAG":     {{"ma", "t1"}, {"mb", "t1"}, {"mc", "t1"}, {"md", "t1"}},
			},
		},
		{
			name:  "q9 triangle shares one forum and a tag",
			query: "lsqb-q9",
			brute: bruteQ9,
			rels: relSet{
				"KNOWS":       {{"a", "b"}, {"b", "c"}, {"c", "a"}},
				"HAS_MEMBER":  {{"f1", "a"}, {"f1", "b"}, {"f1", "c"}, {"f2", "a"}, {"f2", "b"}},
				"HAS_CREATOR": {{"ma", "a"}, {"mb", "b"}, {"mc", "c"}},
				"HAS_TAG":     {{"ma", "t1"}, {"mb", "t1"}, {"mc", "t1"}},
			},
		},
		{
			name:  "q9 triangle shares two forums",
			query: "lsqb-q9",
			brute: bruteQ9,
			rels: relSet{
				"KNOWS":       {{"a", "b"}, {"b", "c"}, {"c", "a"}},
				"HAS_MEMBER":  {{"f1", "a"}, {"f1", "b"}, {"f1", "c"}, {"f2", "a"}, {"f2", "b"}, {"f2", "c"}},
				"HAS_CREATOR": {{"ma", "a"}, {"mb", "b"}, {"mc", "c"}, {"ma2", "a"}},
				"HAS_TAG":     {{"ma", "t1"}, {"mb", "t1"}, {"mc", "t1"}, {"ma2", "t1"}},
			},
		},
		{
			name:  "q9 no common forum",
			query: "lsqb-q9",
			brute: bruteQ9,
			rels: relSet{
				"KNOWS":       {{"a", "b"}, {"b", "c"}, {"c", "a"}},
				"HAS_MEMBER":  {{"f1", "a"}, {"f1", "b"}, {"f2", "c"}},
				"HAS_CREATOR": {{"ma", "a"}, {"mb", "b"}, {"mc", "c"}},
				"HAS_TAG":     {{"ma", "t1"}, {"mb", "t1"}, {"mc", "t1"}},
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
