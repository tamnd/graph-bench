package lsqb

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tamnd/graph-bench/target"
)

// CountOracle computes the reference count for a LSQB count query using an
// engine-independent method over the canonical CSV. It covers the cyclic and
// shared-substructure queries the spec singles out for an independent
// subgraph-counting reference (notes/Spec/2060/bench section 2.5, extended to the
// four-cycle and shared-substructure patterns): Q5 the 3-clique, Q7 the
// four-cycle, Q8 the shared substructure. The tree-shaped joins (Q1-Q4) and the
// dense composite queries (Q6, Q9) still return an error until their oracle is
// wired up.
//
// The returned value is count(*) under Cypher relationship-isomorphism, the same
// quantity the engine returns. That includes the pattern's automorphism
// multiplicity: an undirected triangle is matched six times (3! node orderings
// over a symmetric pattern), so Q5 is six times the distinct-triangle count. The
// four-cycle (Q7) and shared-substructure (Q8) routines already produce the
// full count(*) directly. Verified against gr on a hand-built graph: a square
// with one chord gives Q5 = 12, Q7 = 8, and the two-message fixture gives Q8 = 2.
func CountOracle(queryID string, ds target.Dataset) (int64, error) {
	switch queryID {
	case "lsqb-q5":
		// triangleCount returns distinct triangles; count(*) over the symmetric
		// three-relationship pattern matches each one 3! = 6 times.
		n, err := triangleCount(ds)
		if err != nil {
			return 0, err
		}
		return 6 * n, nil
	case "lsqb-q7":
		return fourCycleCount(ds)
	case "lsqb-q8":
		return sharedSubstructureCount(ds)
	default:
		return 0, fmt.Errorf("lsqb: no oracle for %s (set up a trusted-run reference)", queryID)
	}
}

// triangleCount counts undirected 3-cliques (triangles) over KNOWS edges.
// It builds an adjacency set and uses the forward-only set-intersection
// algorithm: for each edge (a,b) with a < b, count common neighbors c > b.
// Returns the total number of triangles (each counted once).
func triangleCount(ds target.Dataset) (int64, error) {
	files, _, err := ds.RelFiles("KNOWS")
	if err != nil {
		return 0, fmt.Errorf("lsqb: triangle: KNOWS files: %w", err)
	}
	if len(files) == 0 {
		return 0, nil
	}
	adj, err := buildAdjacencySet(files)
	if err != nil {
		return 0, err
	}
	var count int64
	for a, neighbors := range adj {
		for b := range neighbors {
			if b <= a {
				continue
			}
			for c := range neighbors {
				if c <= b {
					continue
				}
				if _, ok := adj[b][c]; ok {
					count++
				}
			}
		}
	}
	return count, nil
}

// fourCycleCount counts matches of Q7, the undirected four-cycle
// a-b-c-d-a over KNOWS, using an engine-independent diagonal method. It builds
// the undirected adjacency and delegates to fourCycleMatches.
func fourCycleCount(ds target.Dataset) (int64, error) {
	files, _, err := ds.RelFiles("KNOWS")
	if err != nil {
		return 0, fmt.Errorf("lsqb: fourcycle: KNOWS files: %w", err)
	}
	if len(files) == 0 {
		return 0, nil
	}
	adj, err := buildAdjacencySet(files)
	if err != nil {
		return 0, err
	}
	return fourCycleMatches(adj), nil
}

// fourCycleMatches returns count(*) for the undirected four-cycle pattern
// a-b-c-d-a over an undirected adjacency set. The pattern has four KNOWS
// relationships, and Cypher relationship-isomorphism requires all four to be
// distinct. On a simple loopless undirected graph any repeated node among
// a,b,c,d would fold two of the four edges into one, so every match has four
// distinct nodes and each simple four-cycle is matched eight times (four
// starting nodes times two directions).
//
// The count is found by diagonals. A four-cycle on {a,b,c,d} is fixed by one
// diagonal pair, say {a,c}, together with its two distinct common neighbors
// {b,d}; so the number of four-cycles whose diagonal is the pair {u,w} is
// C(codeg(u,w), 2), where codeg is the number of common neighbors. Each
// four-cycle has two diagonals, so summing C(codeg,2) over unordered pairs
// counts every four-cycle twice; with eight matches per four-cycle the count(*)
// is 4 times that sum.
//
// codeg(u,w) is the number of wedges u-x-w, tallied by walking each node x and
// incrementing the entry for every unordered pair of x's neighbors.
func fourCycleMatches(adj map[string]map[string]struct{}) int64 {
	codeg := map[[2]string]int64{}
	for x := range adj {
		nbrs := make([]string, 0, len(adj[x]))
		for u := range adj[x] {
			nbrs = append(nbrs, u)
		}
		for i := 0; i < len(nbrs); i++ {
			for j := i + 1; j < len(nbrs); j++ {
				u, w := nbrs[i], nbrs[j]
				if u > w {
					u, w = w, u
				}
				codeg[[2]string{u, w}]++
			}
		}
	}
	var sum int64
	for _, c := range codeg {
		sum += c * (c - 1) / 2 // C(codeg, 2)
	}
	return 4 * sum
}

// sharedSubstructureCount counts matches of Q8: a person p and two distinct
// messages m1, m2 they both created, both carrying a shared tag t. It reads
// HAS_CREATOR (message -> person) and HAS_TAG (message -> tag) and delegates to
// sharedSubstructureMatches.
func sharedSubstructureCount(ds target.Dataset) (int64, error) {
	hcFiles, _, err := ds.RelFiles("HAS_CREATOR")
	if err != nil {
		return 0, fmt.Errorf("lsqb: q8: HAS_CREATOR files: %w", err)
	}
	htFiles, _, err := ds.RelFiles("HAS_TAG")
	if err != nil {
		return 0, fmt.Errorf("lsqb: q8: HAS_TAG files: %w", err)
	}
	creator := map[string]string{}
	for _, f := range hcFiles {
		edges, err := readCSVEdges(f)
		if err != nil {
			return 0, fmt.Errorf("lsqb: read %s: %w", f, err)
		}
		for _, e := range edges {
			creator[e[0]] = e[1] // message -> person
		}
	}
	tagsOf := map[string][]string{}
	for _, f := range htFiles {
		edges, err := readCSVEdges(f)
		if err != nil {
			return 0, fmt.Errorf("lsqb: read %s: %w", f, err)
		}
		for _, e := range edges {
			tagsOf[e[0]] = append(tagsOf[e[0]], e[1]) // message -> tag
		}
	}
	return sharedSubstructureMatches(creator, tagsOf), nil
}

// sharedSubstructureMatches returns count(*) for Q8. The pattern's four
// relationships (two HAS_CREATOR, two HAS_TAG) are automatically distinct once
// m1 and m2 differ, so the only constraint is m1 <> m2. For a fixed person p and
// tag t, let k be the number of messages created by p and tagged t; the ordered
// distinct pairs (m1, m2) number k*(k-1), and each contributes one match for
// that (p, t). The count is the sum of k*(k-1) over all (person, tag) pairs.
//
// creator maps a message to its single creator; tagsOf maps a message to the
// tags attached to it.
func sharedSubstructureMatches(creator map[string]string, tagsOf map[string][]string) int64 {
	k := map[[2]string]int64{}
	for m, p := range creator {
		for _, t := range tagsOf[m] {
			k[[2]string{p, t}]++
		}
	}
	var count int64
	for _, c := range k {
		count += c * (c - 1)
	}
	return count
}

// buildAdjacencySet reads KNOWS relationship CSV files and builds an undirected
// adjacency set keyed by string node id. Both directions are inserted so the
// triangle check works from either endpoint.
func buildAdjacencySet(files []string) (map[string]map[string]struct{}, error) {
	adj := map[string]map[string]struct{}{}
	for _, f := range files {
		edges, err := readCSVEdges(f)
		if err != nil {
			return nil, fmt.Errorf("lsqb: read %s: %w", f, err)
		}
		for _, e := range edges {
			if adj[e[0]] == nil {
				adj[e[0]] = map[string]struct{}{}
			}
			if adj[e[1]] == nil {
				adj[e[1]] = map[string]struct{}{}
			}
			adj[e[0]][e[1]] = struct{}{}
			adj[e[1]][e[0]] = struct{}{}
		}
	}
	return adj, nil
}

// readCSVEdges reads the first two non-empty, non-header columns from a
// relationship CSV file as [start_id, end_id] pairs. It skips the typed header
// line (which contains ":" characters and no real edge data).
func readCSVEdges(path string) ([][2]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseCSVEdges(f)
}

// parseCSVEdges reads [start, end] pairs from a reader, skipping the first line
// (the typed header).
func parseCSVEdges(r io.Reader) ([][2]string, error) {
	sc := bufio.NewScanner(r)
	if !sc.Scan() {
		return nil, nil // empty file
	}
	// First line is the header; skip it.
	var pairs [][2]string
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		// CSV is comma-separated; take the first two fields.
		i := strings.Index(line, ",")
		if i < 0 {
			continue
		}
		start := line[:i]
		rest := line[i+1:]
		j := strings.Index(rest, ",")
		var end string
		if j < 0 {
			end = rest
		} else {
			end = rest[:j]
		}
		if start != "" && end != "" {
			pairs = append(pairs, [2]string{start, end})
		}
	}
	return pairs, sc.Err()
}
