package workload

import (
	"container/heap"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"
)

// This file is the engine-independent oracle for the whole-graph analytics
// kernels: the six LDBC Graphalytics algorithms (spec 07 §4 — BFS, PageRank,
// weakly connected components, community detection by label propagation, local
// clustering coefficient, single-source shortest paths), the GAP additions
// (weighted SSSP, global triangle count, sampled betweenness — spec 07 §4),
// and the Graph500 kernel validation (K2 parent-tree checks, K3 Dijkstra —
// spec 07 §5). Like the reachability oracle in oracle.go, every routine reads
// only the Graph loaded from the canonical CSV and never touches the engine
// under test, because the point of a reference is to catch an engine that is
// fast because it is wrong.
//
// Two determinism choices follow the LDBC specification so two engines'
// outputs validate exactly against one reference: BFS returns the distance
// (level), not an arbitrary parent, and label propagation breaks ties by the
// smallest label. WCC labels a component by the smallest member id for the
// same reason. The synthetic generators emit dense numeric id tokens, so
// results are ordered by the numeric id and the label-valued algorithms report
// the label as that numeric id.

// NodeInt pairs a node id token with an integer result (a BFS level).
type NodeInt struct {
	ID  string
	Val int64
}

// NodeFloat pairs a node id token with a float result (a PageRank score, a
// local clustering coefficient, an SSSP distance, a betweenness score).
type NodeFloat struct {
	ID  string
	Val float64
}

// NodeLabel pairs a node id token with a label result (a component id, a
// propagated community label), the label itself a node id token.
type NodeLabel struct {
	ID    string
	Label int64
}

// nodeOrder returns the node indices sorted by their numeric id, so every
// whole-graph result is emitted in one stable order. A non-numeric id sorts
// by its string form after the numeric ones, which never happens on the
// synthetic datasets these algorithms run against.
func (g *Graph) nodeOrder() []int {
	order := make([]int, len(g.ids))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		ia, ea := strconv.ParseInt(g.ids[order[a]], 10, 64)
		ib, eb := strconv.ParseInt(g.ids[order[b]], 10, 64)
		if ea == nil && eb == nil {
			return ia < ib
		}
		return g.ids[order[a]] < g.ids[order[b]]
	})
	return order
}

// numericID parses a node's id token to an int64, or returns the dense index
// as a fallback so a label is always a number even on a non-numeric dataset.
func (g *Graph) numericID(i int) int64 {
	if v, err := strconv.ParseInt(g.ids[i], 10, 64); err == nil {
		return v
	}
	return int64(i)
}

// BFSLevels returns the breadth-first level (distance in edges) of every node
// reachable from src on the directed graph, ordered by numeric id, with the
// seed at level zero. ok is false when src is unknown. Unreachable nodes are
// omitted, which is the deterministic form: an engine that emits only reached
// vertices and an engine that emits a sentinel for the rest both reduce to
// this set once the sentinels are dropped. It is the Graphalytics BFS
// reference and the Graph500 kernel reference (spec 07 §4–5).
func (g *Graph) BFSLevels(src string) ([]NodeInt, bool) {
	s, ok := g.index[src]
	if !ok {
		return nil, false
	}
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
	}
	var out []NodeInt
	for _, i := range g.nodeOrder() {
		if dist[i] >= 0 {
			out = append(out, NodeInt{ID: g.ids[i], Val: dist[i]})
		}
	}
	return out, true
}

// EdgesReached returns the number of directed edges whose tail is a node
// reachable from src, the edge work a full breadth-first traversal does from
// src. It is the edge count the Graph500 TEPS metric divides by the traversal
// time (spec 07 §5): every edge out of a reached node is examined once. ok is
// false when src is unknown.
func (g *Graph) EdgesReached(src string) (int64, bool) {
	s, ok := g.index[src]
	if !ok {
		return 0, false
	}
	seen := make([]bool, len(g.ids))
	seen[s] = true
	queue := []int{s}
	var edges int64
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		edges += int64(len(g.out[u]))
		for _, v := range g.out[u] {
			if !seen[v] {
				seen[v] = true
				queue = append(queue, v)
			}
		}
	}
	return edges, true
}

// SSSPUnit returns the single-source shortest-path distance from src to every
// reachable node as a float, treating each edge as unit weight, ordered by
// numeric id. ok is false when src is unknown. The unweighted synthetic
// generators emit no edge weights, so the weighted SSSP reduces to the BFS
// distance in float form; it ships as its own query because an engine runs it
// through a different procedure (a weighted shortest path, not a BFS) and is
// measured separately.
func (g *Graph) SSSPUnit(src string) ([]NodeFloat, bool) {
	levels, ok := g.BFSLevels(src)
	if !ok {
		return nil, false
	}
	out := make([]NodeFloat, len(levels))
	for i, l := range levels {
		out[i] = NodeFloat{ID: l.ID, Val: float64(l.Val)}
	}
	return out, true
}

// SSSPWeighted returns the single-source shortest-path distance from src to
// every reachable node over the captured edge weights (the "w" column; 1 when
// the dataset has none), computed by Dijkstra's algorithm, ordered by numeric
// id. ok is false when src is unknown; unreachable nodes are omitted. It is
// the distance-array reference for Graph500 K3 and the GAP SSSP kernel (spec
// 07 §4–5). Weights are assumed non-negative, which the generators guarantee
// (GAP: uniform 1..255).
func (g *Graph) SSSPWeighted(src string) ([]NodeFloat, bool) {
	s, ok := g.index[src]
	if !ok {
		return nil, false
	}
	dist := make([]int64, len(g.ids))
	for i := range dist {
		dist[i] = -1 // -1 marks unreached; every settled distance is >= 0.
	}
	dist[s] = 0
	h := &distHeap{distItem{v: s, d: 0}}
	for h.Len() > 0 {
		it := heap.Pop(h).(distItem)
		if it.d > dist[it.v] {
			continue // stale entry; a shorter path was already settled
		}
		for k, v := range g.out[it.v] {
			nd := it.d + g.outW[it.v][k]
			if dist[v] == -1 || nd < dist[v] {
				dist[v] = nd
				heap.Push(h, distItem{v: v, d: nd})
			}
		}
	}
	var out []NodeFloat
	for _, i := range g.nodeOrder() {
		if dist[i] >= 0 {
			out = append(out, NodeFloat{ID: g.ids[i], Val: float64(dist[i])})
		}
	}
	return out, true
}

// distItem is one tentative distance in the Dijkstra frontier.
type distItem struct {
	v int
	d int64
}

// distHeap is a min-heap of tentative distances for Dijkstra.
type distHeap []distItem

func (h distHeap) Len() int           { return len(h) }
func (h distHeap) Less(i, j int) bool { return h[i].d < h[j].d }
func (h distHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *distHeap) Push(x any)        { *h = append(*h, x.(distItem)) }
func (h *distHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
}

// PageRank returns the PageRank score of every node, ordered by numeric id,
// computed with the LDBC formula: uniform 1/N seed, damping over in-neighbor
// contributions, and the dangling-node mass (nodes with no out-edge)
// redistributed uniformly each iteration. It iterates to the float tolerance
// (the max per-node change falls below tol) or the iteration cap, whichever
// comes first, so the reference is reproducible and matches an engine's
// converged result within the comparison tolerance (spec 07 §1: PageRank
// validates epsilon-based).
func (g *Graph) PageRank(damping float64, tol float64, maxIter int) []NodeFloat {
	n := len(g.ids)
	if n == 0 {
		return nil
	}
	pr := make([]float64, n)
	next := make([]float64, n)
	base := 1.0 / float64(n)
	for i := range pr {
		pr[i] = base
	}
	outdeg := make([]int, n)
	for i := range g.out {
		outdeg[i] = len(g.out[i])
	}
	for iter := 0; iter < maxIter; iter++ {
		var dangling float64
		for i := 0; i < n; i++ {
			if outdeg[i] == 0 {
				dangling += pr[i]
			}
		}
		danglingShare := damping * dangling / float64(n)
		teleport := (1.0 - damping) / float64(n)
		for v := 0; v < n; v++ {
			var sum float64
			for _, u := range g.in[v] {
				sum += pr[u] / float64(outdeg[u])
			}
			next[v] = teleport + danglingShare + damping*sum
		}
		var delta float64
		for i := 0; i < n; i++ {
			d := next[i] - pr[i]
			if d < 0 {
				d = -d
			}
			if d > delta {
				delta = d
			}
		}
		pr, next = next, pr
		if delta < tol {
			break
		}
	}
	out := make([]NodeFloat, 0, n)
	for _, i := range g.nodeOrder() {
		out = append(out, NodeFloat{ID: g.ids[i], Val: pr[i]})
	}
	return out
}

// WeaklyConnectedComponents returns, for every node, the id of its weakly
// connected component, the component labeled by the smallest numeric member
// id, ordered by numeric id. Edges are followed in both directions (weak
// connectivity). It is the Graphalytics WCC reference; the smallest-member
// label makes two engines' component ids comparable without a relabeling
// step.
func (g *Graph) WeaklyConnectedComponents() []NodeLabel {
	n := len(g.ids)
	comp := make([]int, n)
	for i := range comp {
		comp[i] = -1
	}
	var label int
	for s := 0; s < n; s++ {
		if comp[s] != -1 {
			continue
		}
		comp[s] = label
		queue := []int{s}
		for len(queue) > 0 {
			u := queue[0]
			queue = queue[1:]
			for _, v := range g.out[u] {
				if comp[v] == -1 {
					comp[v] = label
					queue = append(queue, v)
				}
			}
			for _, v := range g.in[u] {
				if comp[v] == -1 {
					comp[v] = label
					queue = append(queue, v)
				}
			}
		}
		label++
	}
	// Reduce each component to the smallest numeric member id.
	min := make([]int64, label)
	for i := range min {
		min[i] = -1
	}
	for i := 0; i < n; i++ {
		id := g.numericID(i)
		c := comp[i]
		if min[c] == -1 || id < min[c] {
			min[c] = id
		}
	}
	out := make([]NodeLabel, 0, n)
	for _, i := range g.nodeOrder() {
		out = append(out, NodeLabel{ID: g.ids[i], Label: min[comp[i]]})
	}
	return out
}

// LabelPropagation returns the community label of every node after a fixed
// number of synchronous label-propagation rounds, ties broken by the smallest
// label, the LDBC CDLP determinism rule. Labels start as each node's own
// numeric id; each round a node adopts the most frequent label among its
// neighbors (both directions, a bidirectional pair counting once per
// direction), the smallest label winning a tie. Synchronous update and a
// fixed round count make the result reproducible. It is the Graphalytics CDLP
// reference.
func (g *Graph) LabelPropagation(rounds int) []NodeLabel {
	n := len(g.ids)
	labels := make([]int64, n)
	for i := 0; i < n; i++ {
		labels[i] = g.numericID(i)
	}
	next := make([]int64, n)
	for r := 0; r < rounds; r++ {
		for v := 0; v < n; v++ {
			counts := map[int64]int{}
			for _, u := range g.out[v] {
				counts[labels[u]]++
			}
			for _, u := range g.in[v] {
				counts[labels[u]]++
			}
			if len(counts) == 0 {
				next[v] = labels[v]
				continue
			}
			var best int64
			bestCount := -1
			for lab, c := range counts {
				if c > bestCount || (c == bestCount && lab < best) {
					best = lab
					bestCount = c
				}
			}
			next[v] = best
		}
		labels, next = next, labels
	}
	out := make([]NodeLabel, 0, n)
	for _, i := range g.nodeOrder() {
		out = append(out, NodeLabel{ID: g.ids[i], Label: labels[i]})
	}
	return out
}

// LocalClustering returns the local clustering coefficient of every node,
// ordered by numeric id. For a node v whose neighbor set (the union of out-
// and in-neighbors, excluding v) has size d, the coefficient is the number of
// connected ordered neighbor pairs (u, w) divided by d*(d-1), the count of
// ordered pairs, which is the LDBC directed LCC. A node with fewer than two
// neighbors has coefficient zero. It is the Graphalytics LCC reference.
//
// A pair counts once however many edges run between it. The denominator
// counts pairs, so a numerator counting stored edges would put the RMAT
// graphs, which keep their parallel edges the way Graph500 does, above 1 on
// a well connected node, and a clustering coefficient above 1 is not a
// clustering coefficient. It also means every engine answers the same
// question whether or not its store keeps parallel edges.
func (g *Graph) LocalClustering() []NodeFloat {
	n := len(g.ids)
	// Neighbor sets, undirected union, for membership tests and degree.
	nbr := make([]map[int]struct{}, n)
	for v := 0; v < n; v++ {
		set := map[int]struct{}{}
		for _, u := range g.out[v] {
			if u != v {
				set[u] = struct{}{}
			}
		}
		for _, u := range g.in[v] {
			if u != v {
				set[u] = struct{}{}
			}
		}
		nbr[v] = set
	}
	out := make([]NodeFloat, 0, n)
	for _, v := range g.nodeOrder() {
		d := len(nbr[v])
		if d < 2 {
			out = append(out, NodeFloat{ID: g.ids[v], Val: 0})
			continue
		}
		var links int64
		for u := range nbr[v] {
			// Count neighbors w that u points at, each w once: the
			// adjacency is sorted, so parallel edges are adjacent.
			prev := -1
			for _, w := range g.out[u] {
				if w == u || w == prev {
					continue
				}
				prev = w
				if _, ok := nbr[v][w]; ok {
					links++
				}
			}
		}
		coeff := float64(links) / (float64(d) * float64(d-1))
		out = append(out, NodeFloat{ID: g.ids[v], Val: coeff})
	}
	return out
}

// TriangleCountTotal returns the global undirected triangle count (each
// unordered triple {a,b,c} once), the GAP TC kernel reference (spec 07 §4).
// It is the same quantity UndirectedTriangles computes for micro-triangle-
// undirected; the alias exists so the GAP workload names its oracle by the
// kernel it validates.
func (g *Graph) TriangleCountTotal() int64 {
	return g.UndirectedTriangles()
}

// BetweennessExact returns the betweenness centrality of every node computed
// exactly from the given source subset by Brandes' algorithm on the directed
// graph, ordered by numeric id. Each source is a numeric node id (the id
// token parsed as an integer, the form the GAP source lists use); a source id
// not in the graph contributes nothing. The scores are the unnormalized
// pair-dependency sums over the sampled sources, which is what the GAP BC
// kernel accumulates and what its sampled validation compares (spec 07 §4:
// "BC validated on a subsample exactly").
func (g *Graph) BetweennessExact(sources []int) []NodeFloat {
	n := len(g.ids)
	bc := make([]float64, n)
	for _, sid := range sources {
		s, ok := g.index[strconv.Itoa(sid)]
		if !ok {
			continue
		}
		// Brandes single-source phase: BFS records shortest-path counts
		// (sigma) and predecessors, then dependencies accumulate in reverse
		// BFS order.
		sigma := make([]float64, n)
		dist := make([]int, n)
		preds := make([][]int, n)
		for i := range dist {
			dist[i] = -1
		}
		sigma[s] = 1
		dist[s] = 0
		queue := []int{s}
		var stack []int
		for len(queue) > 0 {
			u := queue[0]
			queue = queue[1:]
			stack = append(stack, u)
			for _, v := range g.out[u] {
				if dist[v] == -1 {
					dist[v] = dist[u] + 1
					queue = append(queue, v)
				}
				if dist[v] == dist[u]+1 {
					sigma[v] += sigma[u]
					preds[v] = append(preds[v], u)
				}
			}
		}
		delta := make([]float64, n)
		for i := len(stack) - 1; i >= 0; i-- {
			w := stack[i]
			for _, v := range preds[w] {
				delta[v] += sigma[v] / sigma[w] * (1 + delta[w])
			}
			if w != s {
				bc[w] += delta[w]
			}
		}
	}
	out := make([]NodeFloat, 0, n)
	for _, i := range g.nodeOrder() {
		out = append(out, NodeFloat{ID: g.ids[i], Val: bc[i]})
	}
	return out
}

// ValidateBFSTree validates a Graph500 K2 BFS parent tree from src against
// the graph, per the spec's checks (spec 07 §5): the root is its own parent,
// every tree edge exists in the graph, and every tree edge connects vertices
// whose BFS levels differ by exactly one. Levels are the oracle's own
// breadth-first distances following edges in both directions, the Graph500
// undirected reading of the generated graph. parent maps a node id token to
// its parent's id token; nodes absent from the map are treated as
// unreached, which the checks do not police (the spec validates the tree,
// the reached-vertex count is TEPS bookkeeping). It returns nil on a valid
// tree and an error describing the first violation in numeric-id order.
func (g *Graph) ValidateBFSTree(src string, parent map[string]string) error {
	s, ok := g.index[src]
	if !ok {
		return fmt.Errorf("bfs tree: unknown source %q", src)
	}
	if p, ok := parent[src]; !ok {
		return fmt.Errorf("bfs tree: root %q has no parent entry, want itself", src)
	} else if p != src {
		return fmt.Errorf("bfs tree: root %q has parent %q, want itself", src, p)
	}

	level := g.undirectedLevels(s)

	// Deterministic first violation: walk the tree's vertices in numeric-id
	// order (string order as the non-numeric fallback, matching nodeOrder).
	keys := make([]string, 0, len(parent))
	for v := range parent {
		keys = append(keys, v)
	}
	sort.Slice(keys, func(a, b int) bool {
		ia, ea := strconv.ParseInt(keys[a], 10, 64)
		ib, eb := strconv.ParseInt(keys[b], 10, 64)
		if ea == nil && eb == nil {
			return ia < ib
		}
		return keys[a] < keys[b]
	})

	for _, v := range keys {
		if v == src {
			continue
		}
		p := parent[v]
		vi, ok := g.index[v]
		if !ok {
			return fmt.Errorf("bfs tree: node %q is not in the graph", v)
		}
		pi, ok := g.index[p]
		if !ok {
			return fmt.Errorf("bfs tree: node %q has parent %q which is not in the graph", v, p)
		}
		if level[vi] == -1 {
			return fmt.Errorf("bfs tree: node %q is in the tree but unreachable from %q", v, src)
		}
		if !g.hasEdgeUndirected(pi, vi) {
			return fmt.Errorf("bfs tree: tree edge %q-%q does not exist in the graph", p, v)
		}
		if level[vi] != level[pi]+1 {
			return fmt.Errorf("bfs tree: tree edge %q-%q connects levels %d and %d, want a difference of one", p, v, level[pi], level[vi])
		}
	}
	return nil
}

// hasEdgeUndirected reports whether an edge exists between u and v in either
// direction, by binary search over the sorted adjacency.
func (g *Graph) hasEdgeUndirected(u, v int) bool {
	return containsSorted(g.out[u], v) || containsSorted(g.out[v], u)
}

// containsSorted reports whether an ascending sorted slice contains x.
func containsSorted(a []int, x int) bool {
	i := sort.SearchInts(a, x)
	return i < len(a) && a[i] == x
}

// PowerScore returns the geometric mean of the given query times in seconds,
// the shape of the SNB BI power score for a single-stream run (spec 07 §2:
// per-query p50 plus the geometric mean of query times). It returns 0 for an
// empty slice or any non-positive time, because a power score over a run with
// a missing or zero-time query is meaningless rather than infinite.
func PowerScore(times []time.Duration) float64 {
	if len(times) == 0 {
		return 0
	}
	var sumLog float64
	for _, t := range times {
		s := t.Seconds()
		if s <= 0 {
			return 0
		}
		sumLog += math.Log(s)
	}
	return math.Exp(sumLog / float64(len(times)))
}
