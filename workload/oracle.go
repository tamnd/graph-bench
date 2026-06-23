package workload

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/tamnd/graph-bench/target"
)

// This file is the reference-answer oracle: the engine-independent routines that
// compute a reference from the canonical CSV (doc 05 section 6.1). They read the
// dataset's node and relationship files directly and never touch the engine under
// test, because the point is to catch an engine that is fast because it is wrong.
// The routines are deliberately simple (a breadth-first walk for reachability, a
// nested intersection for triangles) so they are obviously correct and can be
// cross-checked against the closed-form invariants the grid and ER generators
// record.

// Graph is a directed graph loaded from a dataset's canonical CSV, keyed by the
// opaque :ID token. Nodes are mapped to dense indices for compact adjacency; the
// id token is preserved so an answer can name a node by the same token the
// dataset uses. It models the single-label, single-type synthetic graphs (every
// node label "Node", every relationship type "EDGE"); the id space is global
// across labels, so a multi-label dataset loads into the same structure.
type Graph struct {
	ids   []string       // index -> id token
	index map[string]int // id token -> index
	out   [][]int        // forward adjacency, sorted ascending
	in    [][]int        // backward adjacency, sorted ascending
}

// LoadGraph reads every node file and every relationship type in the dataset's
// schema into a directed Graph. It resolves each relationship endpoint to its
// node index through the global id map; an edge whose endpoint is not a known
// node is an error, because the canonical layout guarantees referential
// integrity and a dangling edge means the data is corrupt, not that the oracle
// should guess.
func LoadGraph(ds target.Dataset) (*Graph, error) {
	g := &Graph{index: map[string]int{}}
	schema := ds.Schema()

	labels := sortedKeys(schema.Nodes)
	for _, label := range labels {
		files, cols, err := ds.NodeFiles(label)
		if err != nil {
			return nil, fmt.Errorf("oracle: node files for %q: %w", label, err)
		}
		idCol, err := structuralColumn(cols, "ID")
		if err != nil {
			return nil, fmt.Errorf("oracle: node label %q: %w", label, err)
		}
		for _, f := range files {
			if err := g.scanNodes(f, idCol); err != nil {
				return nil, err
			}
		}
	}

	types := sortedKeys(schema.Relationships)
	for _, typ := range types {
		files, cols, err := ds.RelFiles(typ)
		if err != nil {
			return nil, fmt.Errorf("oracle: rel files for %q: %w", typ, err)
		}
		startCol, err := structuralColumn(cols, "START_ID")
		if err != nil {
			return nil, fmt.Errorf("oracle: rel type %q: %w", typ, err)
		}
		endCol, err := structuralColumn(cols, "END_ID")
		if err != nil {
			return nil, fmt.Errorf("oracle: rel type %q: %w", typ, err)
		}
		for _, f := range files {
			if err := g.scanRels(f, startCol, endCol); err != nil {
				return nil, err
			}
		}
	}

	g.sortAdjacency()
	return g, nil
}

// NodeCount returns the number of distinct nodes loaded.
func (g *Graph) NodeCount() int { return len(g.ids) }

// EdgeCount returns the number of directed edges loaded.
func (g *Graph) EdgeCount() int {
	var n int
	for _, nbrs := range g.out {
		n += len(nbrs)
	}
	return n
}

// OutDegree returns the out-degree of the node with the given id token, the
// reference for the one-hop expansion (micro-khop1). An unknown id has degree
// zero, the same as an isolated node.
func (g *Graph) OutDegree(id string) int {
	i, ok := g.index[id]
	if !ok {
		return 0
	}
	return len(g.out[i])
}

// ReachableExact returns the number of distinct nodes that are the endpoint of
// some directed walk of length exactly k from the seed: the set obtained by
// applying the out-neighbor relation k times to {seed}. It is the reference for
// the fixed-depth k-hop expansions (micro-khop2 with k=2, micro-khop3 with k=3),
// matching count(DISTINCT c) over a length-k pattern. k=0 is the seed alone.
func (g *Graph) ReachableExact(seed string, k int) int {
	start, ok := g.index[seed]
	if !ok {
		return 0
	}
	frontier := map[int]struct{}{start: {}}
	for hop := 0; hop < k; hop++ {
		next := map[int]struct{}{}
		for u := range frontier {
			for _, v := range g.out[u] {
				next[v] = struct{}{}
			}
		}
		frontier = next
		if len(frontier) == 0 {
			break
		}
	}
	return len(frontier)
}

// ReachableRange returns the number of distinct nodes reachable in between lo and
// hi hops inclusive, the union of the exact-depth sets over the range. It is the
// reference for the variable-length expansion (micro-varlen, R*1..3). The seed
// itself is included only when lo is zero.
func (g *Graph) ReachableRange(seed string, lo, hi int) int {
	start, ok := g.index[seed]
	if !ok {
		return 0
	}
	union := map[int]struct{}{}
	frontier := map[int]struct{}{start: {}}
	for hop := 0; hop <= hi; hop++ {
		if hop >= lo {
			for u := range frontier {
				union[u] = struct{}{}
			}
		}
		if hop == hi {
			break
		}
		next := map[int]struct{}{}
		for u := range frontier {
			for _, v := range g.out[u] {
				next[v] = struct{}{}
			}
		}
		frontier = next
		if len(frontier) == 0 {
			break
		}
	}
	return len(union)
}

// ShortestPath returns the length of the shortest directed path from src to dst
// by breadth-first search, and ok=false when dst is unreachable (or either id is
// unknown). It is the reference for single-pair shortest path (micro-sp); a pair
// with no path has no row in the engine's answer.
func (g *Graph) ShortestPath(src, dst string) (int, bool) {
	s, sok := g.index[src]
	d, dok := g.index[dst]
	if !sok || !dok {
		return 0, false
	}
	if s == d {
		return 0, true
	}
	dist := make([]int, len(g.ids))
	for i := range dist {
		dist[i] = -1
	}
	dist[s] = 0
	queue := []int{s}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		for _, v := range g.out[u] {
			if dist[v] != -1 {
				continue
			}
			dist[v] = dist[u] + 1
			if v == d {
				return dist[v], true
			}
			queue = append(queue, v)
		}
	}
	return 0, false
}

// DirectedTriangles counts distinct directed 3-cycles a->b->c->a over the whole
// graph, the reference for micro-triangle. It iterates each edge a->b and
// intersects b's out-neighbors with a's in-neighbors (the c that closes the
// cycle), which is the adjacency-intersection that bounds the work by the output
// rather than materializing every 2-path. Both adjacency lists are sorted, so the
// intersection is a linear merge. Each cycle is found once per constituent edge,
// so the raw per-edge total is exactly three times the number of distinct cycles
// (the three rotations a->b->c, b->c->a, c->a->b); the routine divides by three
// and returns the distinct-cycle count, which is what "the triangle count" means.
func (g *Graph) DirectedTriangles() int64 {
	var raw int64
	for a := range g.out {
		for _, b := range g.out[a] {
			raw += int64(intersectionSize(g.out[b], g.in[a]))
		}
	}
	return raw / 3
}

// UndirectedTriangles counts triangles in the undirected graph (each unordered
// triple {a,b,c} once), the reference for micro-triangle-undirected. It builds
// the undirected adjacency once, then for each node with degree at least two
// counts pairs of higher-indexed neighbors that are themselves adjacent, the
// standard each-triangle-once enumeration.
func (g *Graph) UndirectedTriangles() int64 {
	adj := make([]map[int]struct{}, len(g.ids))
	for i := range adj {
		adj[i] = map[int]struct{}{}
	}
	for a := range g.out {
		for _, b := range g.out[a] {
			if a == b {
				continue
			}
			adj[a][b] = struct{}{}
			adj[b][a] = struct{}{}
		}
	}
	// Order each node's neighbors so each triangle is counted once: for an edge
	// (a,b) with a<b, count common neighbors c with c>b.
	higher := make([][]int, len(g.ids))
	for a := range adj {
		for b := range adj[a] {
			if b > a {
				higher[a] = append(higher[a], b)
			}
		}
		sort.Ints(higher[a])
	}
	var count int64
	for a := range higher {
		for _, b := range higher[a] {
			for _, c := range higher[b] {
				if _, ok := adj[a][c]; ok {
					count++
				}
			}
		}
	}
	return count
}

// scanNodes reads one node file and registers each id token. The first record is
// the header and is skipped.
func (g *Graph) scanNodes(path string, idCol int) error {
	return scanCSV(path, func(rec []string) error {
		if idCol >= len(rec) {
			return fmt.Errorf("oracle: node row in %s has %d fields, need id at %d", path, len(rec), idCol)
		}
		g.intern(rec[idCol])
		return nil
	})
}

// scanRels reads one relationship file and adds each edge, resolving both
// endpoints through the id map.
func (g *Graph) scanRels(path string, startCol, endCol int) error {
	return scanCSV(path, func(rec []string) error {
		if startCol >= len(rec) || endCol >= len(rec) {
			return fmt.Errorf("oracle: rel row in %s has %d fields, need endpoints at %d,%d", path, len(rec), startCol, endCol)
		}
		s, ok := g.index[rec[startCol]]
		if !ok {
			return fmt.Errorf("oracle: edge in %s references unknown start id %q", path, rec[startCol])
		}
		e, ok := g.index[rec[endCol]]
		if !ok {
			return fmt.Errorf("oracle: edge in %s references unknown end id %q", path, rec[endCol])
		}
		g.out[s] = append(g.out[s], e)
		g.in[e] = append(g.in[e], s)
		return nil
	})
}

// intern returns the dense index for an id token, assigning a new one on first
// sight and growing the adjacency slices to match.
func (g *Graph) intern(id string) int {
	if i, ok := g.index[id]; ok {
		return i
	}
	i := len(g.ids)
	g.index[id] = i
	g.ids = append(g.ids, id)
	g.out = append(g.out, nil)
	g.in = append(g.in, nil)
	return i
}

// sortAdjacency sorts every adjacency list ascending so the triangle
// intersection is a linear merge and the reachability frontier is deterministic.
func (g *Graph) sortAdjacency() {
	for i := range g.out {
		sort.Ints(g.out[i])
	}
	for i := range g.in {
		sort.Ints(g.in[i])
	}
}

// scanCSV opens a CSV file and calls fn for each data row, skipping the header.
// It uses a comma separator and tolerates a variable field count so a row with a
// trailing empty cell is read as written.
func scanCSV(path string, fn func(rec []string) error) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("oracle: open %s: %w", path, err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.ReuseRecord = true
	first := true
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("oracle: read %s: %w", path, err)
		}
		if first {
			first = false
			continue
		}
		if err := fn(rec); err != nil {
			return err
		}
	}
}

// structuralColumn returns the index of the column with the given structural type
// (ID, START_ID, END_ID), or an error naming what was missing.
func structuralColumn(cols []target.Column, typ string) (int, error) {
	for i, c := range cols {
		if c.Type == typ {
			return i, nil
		}
	}
	return 0, fmt.Errorf("header has no :%s column", typ)
}

// intersectionSize returns the number of common elements of two ascending sorted
// slices by a linear merge.
func intersectionSize(a, b []int) int {
	var n, i, j int
	for i < len(a) && j < len(b) {
		switch {
		case a[i] < b[j]:
			i++
		case a[i] > b[j]:
			j++
		default:
			n++
			i++
			j++
		}
	}
	return n
}

// sortedKeys returns the keys of a node-schema or rel-schema map in sorted order.
func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
