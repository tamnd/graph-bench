package workload

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/tamnd/graph-bench/engine"
)

// This file is the reference-answer oracle: the engine-independent routines
// that compute a reference from the canonical CSV (spec 08 §5, ADR-9). They
// read the dataset's node and relationship files directly and never touch the
// engine under test, because the point is to catch an engine that is fast
// because it is wrong. The routines are deliberately simple (a breadth-first
// walk for reachability, a nested intersection for triangles) so they are
// obviously correct and can be cross-checked against the closed-form
// invariants the grid and ER generators record.

// Graph is a directed graph loaded from a dataset's canonical CSV, keyed by
// the opaque :ID token. Nodes are mapped to dense indices for compact
// adjacency; the id token is preserved so an answer can name a node by the
// same token the dataset uses. It models the single-label, single-type
// synthetic graphs (every node label "Node", every relationship type "EDGE");
// the id space is global across labels, so a multi-label dataset loads into
// the same structure.
//
// When a relationship file carries an integer weight column named "w" (the
// GAP/Graph500 weighted graphs, spec 07 §4–5), the per-edge weight is captured
// alongside the adjacency for the weighted-SSSP oracles; a dataset without the
// column loads with every weight 1, so the unweighted graphs need no special
// case.
type Graph struct {
	ids   []string       // index -> id token
	index map[string]int // id token -> index
	out   [][]int        // forward adjacency, sorted ascending
	in    [][]int        // backward adjacency, sorted ascending
	outW  [][]int64      // per-edge weight, parallel to out ("w" column; 1 when absent)
}

// LoadGraph reads every node file and every relationship type in the
// dataset's schema into a directed Graph. It resolves each relationship
// endpoint to its node index through the global id map; an edge whose
// endpoint is not a known node is an error, because the canonical layout
// guarantees referential integrity and a dangling edge means the data is
// corrupt, not that the oracle should guess. Structural columns are located
// through the typed CSV header (spec 05 §1: `id:ID`, `:START_ID`, `:END_ID`).
func LoadGraph(ds engine.Dataset) (*Graph, error) {
	g := &Graph{index: map[string]int{}}
	schema := ds.Schema()

	for _, label := range sortedKeys(schema.Nodes) {
		files, err := ds.NodeFiles(label)
		if err != nil {
			return nil, fmt.Errorf("oracle: node files for %q: %w", label, err)
		}
		for _, f := range files {
			if err := g.scanNodes(f); err != nil {
				return nil, err
			}
		}
	}

	for _, typ := range sortedKeys(schema.Rels) {
		files, err := ds.RelFiles(typ)
		if err != nil {
			return nil, fmt.Errorf("oracle: rel files for %q: %w", typ, err)
		}
		for _, f := range files {
			if err := g.scanRels(f); err != nil {
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

// HasNode reports whether the dataset contains a node with the given id
// token. It is the reference for the point lookup (micro-point) and its
// negative variant (micro-point-miss): the lookup returns one row when the id
// exists and zero rows when it does not.
func (g *Graph) HasNode(id string) bool {
	_, ok := g.index[id]
	return ok
}

// ReachableExact returns the number of distinct nodes that are the endpoint
// of some directed walk of length exactly k from the seed: the set obtained
// by applying the out-neighbor relation k times to {seed}. It is the
// reference for the fixed-depth k-hop expansions (micro-khop2 with k=2,
// micro-khop3 with k=3), matching count(DISTINCT c) over a length-k pattern.
// k=0 is the seed alone.
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

// ReachableRange returns the number of distinct nodes reachable in between lo
// and hi hops inclusive, the union of the exact-depth sets over the range. It
// is the reference for the variable-length expansion (micro-varlen, R*1..3).
// The seed itself is included only when lo is zero.
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

// ShortestPath returns the length of the shortest directed path from src to
// dst by breadth-first search, and ok=false when dst is unreachable (or
// either id is unknown). It is the reference for single-pair shortest path
// (micro-sp); a pair with no path has no row in the engine's answer.
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

// ShortestPathUndirected returns the length of the shortest path from src to
// dst treating every edge as undirected (the union of out- and in-neighbors
// at each step), and ok=false when dst is unreachable or either id is
// unknown. It is the reference for bidirectional shortest path
// (micro-sp-bidir): the same length a meet-in-the-middle search finds,
// computed here by a plain breadth-first walk so the reference is obviously
// correct regardless of how the engine grows its two frontiers.
func (g *Graph) ShortestPathUndirected(src, dst string) (int, bool) {
	s, sok := g.index[src]
	d, dok := g.index[dst]
	if !sok || !dok {
		return 0, false
	}
	if s == d {
		return 0, true
	}
	dist := g.undirectedLevels(s)
	if dist[d] == -1 {
		return 0, false
	}
	return int(dist[d]), true
}

// undirectedLevels returns the breadth-first distance from s to every node
// following edges in both directions, -1 for unreachable. Shared by the
// undirected shortest path and the Graph500 BFS-tree validation.
func (g *Graph) undirectedLevels(s int) []int64 {
	dist := make([]int64, len(g.ids))
	for i := range dist {
		dist[i] = -1
	}
	dist[s] = 0
	queue := []int{s}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		for _, v := range g.out[u] {
			if dist[v] == -1 {
				dist[v] = dist[u] + 1
				queue = append(queue, v)
			}
		}
		for _, v := range g.in[u] {
			if dist[v] == -1 {
				dist[v] = dist[u] + 1
				queue = append(queue, v)
			}
		}
	}
	return dist
}

// ScanIDStats returns the count of nodes and the sum of their numeric id
// values over the whole graph, the reference for the property scan and
// aggregate (micro-scan). The synthetic generators emit a dense numeric id,
// so the scan aggregates that id column: the engine's count(n) matches the
// node count and its avg(n.id) matches sum/count. A node whose id token is
// not a base-10 integer is counted but contributes zero to the sum, which
// never happens on the synthetic datasets this query runs against.
func (g *Graph) ScanIDStats() (count int64, sum int64) {
	for _, id := range g.ids {
		count++
		if v, err := strconv.ParseInt(id, 10, 64); err == nil {
			sum += v
		}
	}
	return count, sum
}

// DirectedTriangles counts distinct directed 3-cycles a->b->c->a over the
// whole graph, the reference for micro-triangle. It iterates each edge a->b
// and intersects b's out-neighbors with a's in-neighbors (the c that closes
// the cycle), which is the adjacency-intersection that bounds the work by the
// output rather than materializing every 2-path. Both adjacency lists are
// sorted, so the intersection is a linear merge. Each cycle is found once per
// constituent edge, so the raw per-edge total is exactly three times the
// number of distinct cycles (the three rotations a->b->c, b->c->a, c->a->b);
// the routine divides by three and returns the distinct-cycle count, which is
// what "the triangle count" means.
func (g *Graph) DirectedTriangles() int64 {
	var raw int64
	for a := range g.out {
		for _, b := range g.out[a] {
			raw += int64(intersectionSize(g.out[b], g.in[a]))
		}
	}
	return raw / 3
}

// UndirectedTriangles counts triangles in the undirected graph (each
// unordered triple {a,b,c} once), the reference for micro-triangle-undirected
// and the GAP triangle-count kernel. It builds the undirected adjacency once,
// then for each node counts pairs of higher-indexed neighbors that are
// themselves adjacent, the standard each-triangle-once enumeration.
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
	// Order each node's neighbors so each triangle is counted once: for an
	// edge (a,b) with a<b, count common neighbors c with c>b.
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

// scanNodes reads one node file, locating the :ID column from the typed
// header and registering each id token.
func (g *Graph) scanNodes(path string) error {
	idCol := -1
	return scanCSV(path,
		func(hdr []string) error {
			idCol = structuralColumn(hdr, "ID")
			if idCol < 0 {
				return fmt.Errorf("oracle: node file %s header has no :ID column", path)
			}
			return nil
		},
		func(rec []string) error {
			if idCol >= len(rec) {
				return fmt.Errorf("oracle: node row in %s has %d fields, need id at %d", path, len(rec), idCol)
			}
			g.intern(rec[idCol])
			return nil
		})
}

// scanRels reads one relationship file, locating the :START_ID and :END_ID
// columns (and the optional "w" weight column) from the typed header, and
// adds each edge, resolving both endpoints through the id map.
func (g *Graph) scanRels(path string) error {
	startCol, endCol, wCol := -1, -1, -1
	return scanCSV(path,
		func(hdr []string) error {
			startCol = structuralColumn(hdr, "START_ID")
			endCol = structuralColumn(hdr, "END_ID")
			wCol = namedColumn(hdr, "w")
			if startCol < 0 || endCol < 0 {
				return fmt.Errorf("oracle: rel file %s header has no :START_ID/:END_ID columns", path)
			}
			return nil
		},
		func(rec []string) error {
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
			w := int64(1)
			if wCol >= 0 {
				if wCol >= len(rec) {
					return fmt.Errorf("oracle: rel row in %s has %d fields, need weight at %d", path, len(rec), wCol)
				}
				var err error
				w, err = strconv.ParseInt(rec[wCol], 10, 64)
				if err != nil {
					return fmt.Errorf("oracle: edge in %s has non-integer weight %q: %w", path, rec[wCol], err)
				}
			}
			g.out[s] = append(g.out[s], e)
			g.outW[s] = append(g.outW[s], w)
			g.in[e] = append(g.in[e], s)
			return nil
		})
}

// intern returns the dense index for an id token, assigning a new one on
// first sight and growing the adjacency slices to match.
func (g *Graph) intern(id string) int {
	if i, ok := g.index[id]; ok {
		return i
	}
	i := len(g.ids)
	g.index[id] = i
	g.ids = append(g.ids, id)
	g.out = append(g.out, nil)
	g.in = append(g.in, nil)
	g.outW = append(g.outW, nil)
	return i
}

// sortAdjacency sorts every adjacency list ascending so the triangle
// intersection is a linear merge and the reachability frontier is
// deterministic. The out list carries its parallel weight slice through the
// sort so weights stay attached to their edges.
func (g *Graph) sortAdjacency() {
	for i := range g.out {
		sort.Sort(&edgeSort{to: g.out[i], w: g.outW[i]})
	}
	for i := range g.in {
		sort.Ints(g.in[i])
	}
}

// edgeSort sorts an out-adjacency list and its parallel weight slice together
// by target index (then weight, for a deterministic order of parallel edges).
type edgeSort struct {
	to []int
	w  []int64
}

func (e *edgeSort) Len() int { return len(e.to) }
func (e *edgeSort) Less(a, b int) bool {
	if e.to[a] != e.to[b] {
		return e.to[a] < e.to[b]
	}
	return e.w[a] < e.w[b]
}
func (e *edgeSort) Swap(a, b int) {
	e.to[a], e.to[b] = e.to[b], e.to[a]
	e.w[a], e.w[b] = e.w[b], e.w[a]
}

// scanCSV opens a CSV file, calls header with the first record, then row for
// each subsequent record. It uses a comma separator and tolerates a variable
// field count so a row with a trailing empty cell is read as written. The
// record slice is reused between calls; callbacks must not retain it.
func scanCSV(path string, header func(rec []string) error, row func(rec []string) error) error {
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
			if err := header(rec); err != nil {
				return err
			}
			continue
		}
		if err := row(rec); err != nil {
			return err
		}
	}
}

// splitHeaderField splits a typed CSV header field "name:TYPE" (spec 05 §1)
// into its name and type parts; a field with no colon is a bare property name
// with an empty type.
func splitHeaderField(field string) (name, typ string) {
	if i := strings.LastIndex(field, ":"); i >= 0 {
		return field[:i], field[i+1:]
	}
	return field, ""
}

// structuralColumn returns the index of the header column with the given
// structural type (ID, START_ID, END_ID), or -1 when the header has none.
func structuralColumn(hdr []string, typ string) int {
	for i, f := range hdr {
		if _, t := splitHeaderField(f); t == typ {
			return i
		}
	}
	return -1
}

// namedColumn returns the index of the header column with the given property
// name, or -1 when the header has none.
func namedColumn(hdr []string, name string) int {
	for i, f := range hdr {
		if n, _ := splitHeaderField(f); n == name {
			return i
		}
	}
	return -1
}

// intersectionSize returns the number of common elements of two ascending
// sorted slices by a linear merge.
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

// sortedKeys returns the keys of a node-schema or rel-schema map in sorted
// order.
func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
