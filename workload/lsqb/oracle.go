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
// engine-independent method over the canonical CSV. For the 3-clique (Q5) it
// uses the set-intersection algorithm. Other queries return an error until their
// oracle is wired up with real SNB data in the integration path.
func CountOracle(queryID string, ds target.Dataset) (int64, error) {
	switch queryID {
	case "lsqb-q5":
		return triangleCount(ds)
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
