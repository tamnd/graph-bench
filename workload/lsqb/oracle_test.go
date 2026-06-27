package lsqb

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/tamnd/graph-bench/target"
)

// fileDataset is a minimal target.Dataset that serves a fixed set of CSV file
// paths per relationship type. The embedded nil interface satisfies the methods
// the oracle never calls; only RelFiles is implemented.
type fileDataset struct {
	target.Dataset
	rels map[string][]string
}

func (d fileDataset) RelFiles(typ string) ([]string, []target.Column, error) {
	return d.rels[typ], nil, nil
}

// writeCSV writes a header line followed by the given comma-joined rows and
// returns the path, mirroring the canonical relationship CSV shape the oracle
// reads (header skipped, first two columns taken as start,end).
func writeCSV(t *testing.T, dir, name, header string, rows [][2]string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	var b []byte
	b = append(b, header...)
	b = append(b, '\n')
	for _, r := range rows {
		b = append(b, r[0]...)
		b = append(b, ',')
		b = append(b, r[1]...)
		b = append(b, '\n')
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// undirectedAdj builds the undirected adjacency set from a list of edges, the
// same shape buildAdjacencySet produces from CSV. Each edge is inserted both
// ways.
func undirectedAdj(edges [][2]string) map[string]map[string]struct{} {
	adj := map[string]map[string]struct{}{}
	add := func(a, b string) {
		if adj[a] == nil {
			adj[a] = map[string]struct{}{}
		}
		adj[a][b] = struct{}{}
	}
	for _, e := range edges {
		add(e[0], e[1])
		add(e[1], e[0])
	}
	return adj
}

// bruteFourCycle counts Q7 matches directly: ordered node 4-tuples (a,b,c,d)
// with all four undirected KNOWS edges present and pairwise distinct as
// undirected edges. This is the literal reading of the pattern under
// relationship-isomorphism and is O(n^4), fine for the small test graphs.
func bruteFourCycle(adj map[string]map[string]struct{}) int64 {
	nodes := make([]string, 0, len(adj))
	for n := range adj {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	has := func(a, b string) bool {
		_, ok := adj[a][b]
		return ok
	}
	// canonical undirected edge key
	key := func(a, b string) [2]string {
		if a > b {
			a, b = b, a
		}
		return [2]string{a, b}
	}
	var count int64
	for _, a := range nodes {
		for _, b := range nodes {
			if !has(a, b) {
				continue
			}
			for _, c := range nodes {
				if !has(b, c) {
					continue
				}
				for _, d := range nodes {
					if !has(c, d) || !has(d, a) {
						continue
					}
					// the four edges must be four distinct undirected edges
					es := map[[2]string]struct{}{
						key(a, b): {},
						key(b, c): {},
						key(c, d): {},
						key(d, a): {},
					}
					if len(es) == 4 {
						count++
					}
				}
			}
		}
	}
	return count
}

// bruteSharedSubstructure counts Q8 matches directly: ordered (p,m1,t,m2) with
// m1 != m2, both messages created by p, both tagged t. O(messages^2 * tags).
func bruteSharedSubstructure(creator map[string]string, tagsOf map[string][]string) int64 {
	tagSet := map[string]map[string]struct{}{}
	for m, ts := range tagsOf {
		s := map[string]struct{}{}
		for _, t := range ts {
			s[t] = struct{}{}
		}
		tagSet[m] = s
	}
	msgs := make([]string, 0, len(creator))
	for m := range creator {
		msgs = append(msgs, m)
	}
	sort.Strings(msgs)
	var count int64
	for _, m1 := range msgs {
		for _, m2 := range msgs {
			if m1 == m2 || creator[m1] != creator[m2] {
				continue
			}
			for t := range tagSet[m1] {
				if _, ok := tagSet[m2][t]; ok {
					count++
				}
			}
		}
	}
	return count
}

// TestCountOracleEndToEnd drives CountOracle through the CSV-reading path for
// the three wired queries, so the RelFiles plumbing, header skipping, and
// adjacency build are exercised, not just the pure counters.
func TestCountOracleEndToEnd(t *testing.T) {
	dir := t.TempDir()

	// A four-node square plus a chord a-c: one square (b,d common to a,c) gives
	// count(*) = 8; the chord adds no new four-cycle on these four nodes.
	knows := writeCSV(t, dir, "knows.csv", ":START_ID,:END_ID,creationDate", [][2]string{
		{"a", "b"}, {"b", "c"}, {"c", "d"}, {"d", "a"}, {"a", "c"},
	})
	// Two messages by p1 sharing tag t1; q8 count(*) = k*(k-1) = 2.
	hasCreator := writeCSV(t, dir, "has_creator.csv", ":START_ID,:END_ID", [][2]string{
		{"m1", "p1"}, {"m2", "p1"},
	})
	hasTag := writeCSV(t, dir, "has_tag.csv", ":START_ID,:END_ID", [][2]string{
		{"m1", "t1"}, {"m2", "t1"},
	})

	ds := fileDataset{rels: map[string][]string{
		"KNOWS":       {knows},
		"HAS_CREATOR": {hasCreator},
		"HAS_TAG":     {hasTag},
	}}

	cases := []struct {
		query string
		want  int64
	}{
		{"lsqb-q5", 12}, // 2 distinct triangles (a-b-c, a-c-d) times 6 automorphisms
		{"lsqb-q7", 8},  // 1 distinct four-cycle (a-b-c-d) times 8 automorphisms
		{"lsqb-q8", 2},  // 2 messages by p1 sharing t1: ordered pairs = 2*1
	}
	for _, tc := range cases {
		got, err := CountOracle(tc.query, ds)
		if err != nil {
			t.Fatalf("%s: %v", tc.query, err)
		}
		if got != tc.want {
			t.Errorf("%s: CountOracle=%d, want %d", tc.query, got, tc.want)
		}
	}

	// Every query id now has an oracle; one with no relevant edges in this fixture
	// counts zero without error, and an unknown id still errors.
	for _, id := range []string{"lsqb-q1", "lsqb-q2", "lsqb-q3", "lsqb-q4", "lsqb-q6", "lsqb-q9"} {
		got, err := CountOracle(id, ds)
		if err != nil {
			t.Errorf("%s: %v", id, err)
		}
		if got != 0 {
			t.Errorf("%s on the cyclic fixture: got %d, want 0", id, got)
		}
	}
	if _, err := CountOracle("lsqb-q99", ds); err == nil {
		t.Error("lsqb-q99 should have no oracle")
	}
}

func TestFourCycleMatchesAgainstBruteForce(t *testing.T) {
	cases := []struct {
		name  string
		edges [][2]string
	}{
		{"empty", nil},
		{"single edge, no cycle", [][2]string{{"a", "b"}}},
		{"triangle, no four-cycle", [][2]string{{"a", "b"}, {"b", "c"}, {"c", "a"}}},
		{"one square", [][2]string{{"a", "b"}, {"b", "c"}, {"c", "d"}, {"d", "a"}}},
		{"square with a chord", [][2]string{{"a", "b"}, {"b", "c"}, {"c", "d"}, {"d", "a"}, {"a", "c"}}},
		{"complete K4", [][2]string{{"a", "b"}, {"a", "c"}, {"a", "d"}, {"b", "c"}, {"b", "d"}, {"c", "d"}}},
		{"two squares sharing an edge", [][2]string{
			{"a", "b"}, {"b", "c"}, {"c", "d"}, {"d", "a"},
			{"b", "e"}, {"e", "f"}, {"f", "c"},
		}},
		{"K_{2,3} bipartite", [][2]string{
			{"u1", "w1"}, {"u1", "w2"}, {"u1", "w3"},
			{"u2", "w1"}, {"u2", "w2"}, {"u2", "w3"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adj := undirectedAdj(tc.edges)
			got := fourCycleMatches(adj)
			want := bruteFourCycle(adj)
			if got != want {
				t.Errorf("fourCycleMatches=%d, brute force=%d", got, want)
			}
		})
	}
}

func TestSharedSubstructureMatchesAgainstBruteForce(t *testing.T) {
	cases := []struct {
		name    string
		creator map[string]string
		tags    map[string][]string
	}{
		{"empty", map[string]string{}, map[string][]string{}},
		{
			"one message cannot pair",
			map[string]string{"m1": "p1"},
			map[string][]string{"m1": {"t1"}},
		},
		{
			"two messages, one shared tag",
			map[string]string{"m1": "p1", "m2": "p1"},
			map[string][]string{"m1": {"t1", "t2"}, "m2": {"t1"}},
		},
		{
			"two messages, two shared tags",
			map[string]string{"m1": "p1", "m2": "p1"},
			map[string][]string{"m1": {"t1", "t2"}, "m2": {"t1", "t2"}},
		},
		{
			"different creators do not pair",
			map[string]string{"m1": "p1", "m2": "p2"},
			map[string][]string{"m1": {"t1"}, "m2": {"t1"}},
		},
		{
			"three messages one tag, same creator",
			map[string]string{"m1": "p1", "m2": "p1", "m3": "p1"},
			map[string][]string{"m1": {"t1"}, "m2": {"t1"}, "m3": {"t1"}},
		},
		{
			"mixed creators and tags",
			map[string]string{"m1": "p1", "m2": "p1", "m3": "p1", "m4": "p2", "m5": "p2"},
			map[string][]string{
				"m1": {"t1", "t2"},
				"m2": {"t1"},
				"m3": {"t2", "t3"},
				"m4": {"t1", "t2"},
				"m5": {"t2"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sharedSubstructureMatches(tc.creator, tc.tags)
			want := bruteSharedSubstructure(tc.creator, tc.tags)
			if got != want {
				t.Errorf("sharedSubstructureMatches=%d, brute force=%d", got, want)
			}
		})
	}
}
