package lsqb

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/tamnd/graph-bench/engine"
)

// fileDataset is a minimal engine.Dataset that serves a fixed set of CSV file
// paths per relationship type. The embedded nil interface satisfies the methods
// the oracle never calls; only RelFiles is implemented.
type fileDataset struct {
	engine.Dataset
	rels map[string][]string
}

func (d fileDataset) RelFiles(typ string) ([]string, error) {
	return d.rels[typ], nil
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
			got := fourCycleMatches(tc.edges)
			want := bruteFourCycle(adj)
			if got != want {
				t.Errorf("fourCycleMatches=%d, brute force=%d", got, want)
			}
		})
	}
}
