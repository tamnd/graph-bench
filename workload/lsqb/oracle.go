package lsqb

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tamnd/graph-bench/engine"
)

// This file is the counting oracle: the engine-independent routines that
// compute each query's count(*) straight from the canonical relationship CSV
// (ADR-9), never through another engine. The returned value is count(*) under
// Cypher relationship-isomorphism, the same quantity the engine returns:
// relationships in a pattern must be pairwise distinct, but nodes may
// coincide. That semantics decides the automorphism multiplicity: the
// undirected KNOWS triangle is matched six times per distinct triangle (3!
// node orderings over a symmetric pattern), the undirected four-cycle eight
// times per simple cycle (plus the degenerate matches parallel KNOWS edges
// admit, which the q7 enumerator handles by enumerating concrete
// relationships), and the tree joins q1-q4 have no symmetry, so their match
// count is the product of independent fan-outs at each branch.
//
// The social generator emits KNOWS as directed rows, and a pair of persons
// may know each other in both directions: two distinct relationships between
// one unordered pair. The undirected patterns count one match per choice of
// concrete relationship, so the triangle oracles carry the per-pair edge
// multiplicity through the product rather than assuming a simple graph.
//
// One generator invariant is relied on and worth naming: every Post has
// exactly one HAS_CREATOR edge, so the posts attached to distinct persons in
// a pattern are automatically distinct and the CONTAINER_OF/LIKES
// relationships hanging off them are too.

// CountOracle computes the reference count for an LSQB query id over the
// dataset's canonical CSV.
func CountOracle(queryID string, ds engine.Dataset) (int64, error) {
	switch queryID {
	case "lsqb-q1":
		return q1Count(ds)
	case "lsqb-q2":
		return q2Count(ds)
	case "lsqb-q3":
		return q3Count(ds)
	case "lsqb-q4":
		return q4Count(ds)
	case "lsqb-q5":
		return q5Count(ds)
	case "lsqb-q6":
		return q6Count(ds)
	case "lsqb-q7":
		return q7Count(ds)
	case "lsqb-q8":
		return q8Count(ds)
	case "lsqb-q9":
		return q9Count(ds)
	default:
		return 0, fmt.Errorf("lsqb: no oracle for %s", queryID)
	}
}

// q1Count: (f:Forum)-[:CONTAINER_OF]->(m:Post)-[:HAS_CREATOR]->(p:Person)
// -[:LIKES]->(:Post). A chain through the containment hub: for each
// containment edge, the creator's LIKES fan-out.
func q1Count(ds engine.Dataset) (int64, error) {
	containerOf, err := loadEdges(ds, "CONTAINER_OF")
	if err != nil {
		return 0, err
	}
	hasCreator, err := loadEdges(ds, "HAS_CREATOR")
	if err != nil {
		return 0, err
	}
	likes, err := loadEdges(ds, "LIKES")
	if err != nil {
		return 0, err
	}
	creators := groupStarts(hasCreator)
	likesDeg := startDegree(likes)

	var count int64
	for _, e := range containerOf {
		for _, p := range creators[e[1]] {
			count += likesDeg[p]
		}
	}
	return count, nil
}

// q2Count: (m:Post)-[:HAS_CREATOR]->(p:Person)-[:KNOWS]->(:Person) together
// with (m)<-[:LIKES]-(:Person). The post is the hub with two independent
// fan-outs: the creator's out-friendships and the post's likers. For each
// post the match count is the product.
func q2Count(ds engine.Dataset) (int64, error) {
	hasCreator, err := loadEdges(ds, "HAS_CREATOR")
	if err != nil {
		return 0, err
	}
	knows, err := loadEdges(ds, "KNOWS")
	if err != nil {
		return 0, err
	}
	likes, err := loadEdges(ds, "LIKES")
	if err != nil {
		return 0, err
	}
	creators := groupStarts(hasCreator)
	knowsDeg := startDegree(knows)
	likesIn := endDegree(likes)

	var count int64
	for m, ps := range creators {
		var friendChain int64
		for _, p := range ps {
			friendChain += knowsDeg[p]
		}
		count += friendChain * likesIn[m]
	}
	return count, nil
}

// q3Count: (f:Forum)-[:HAS_MEMBER]->(p:Person), (f)-[:CONTAINER_OF]->(m:Post)
// -[:HAS_CREATOR]->(p). The bound person is shared between the member edge
// and the post's creator, so this is a diamond, not a pure tree: for each
// forum, each contained post whose creator is a member contributes one match
// per member edge.
func q3Count(ds engine.Dataset) (int64, error) {
	hasMember, err := loadEdges(ds, "HAS_MEMBER")
	if err != nil {
		return 0, err
	}
	containerOf, err := loadEdges(ds, "CONTAINER_OF")
	if err != nil {
		return 0, err
	}
	hasCreator, err := loadEdges(ds, "HAS_CREATOR")
	if err != nil {
		return 0, err
	}
	memberCount := pairCounts(hasMember) // {forum, person} -> member-edge count
	contains := groupStarts(containerOf)
	creators := groupStarts(hasCreator)

	var count int64
	for f, ms := range contains {
		for _, m := range ms {
			for _, p := range creators[m] {
				count += memberCount[arcKey(f, p)]
			}
		}
	}
	return count, nil
}

// q4Count: (m:Post)-[:HAS_CREATOR]->(:Person)-[:KNOWS]->(:Person)-[:KNOWS]->
// (:Person), (m)<-[:LIKES]-(:Person), (m)<-[:CONTAINER_OF]-(:Forum). The
// deeper tree: a three-level chain out of the post hub plus two independent
// one-hop branches into it; the match count per post is the product of the
// three. The two chain relationships are always distinct (their sources
// differ), so the chain length is a plain nested sum.
func q4Count(ds engine.Dataset) (int64, error) {
	hasCreator, err := loadEdges(ds, "HAS_CREATOR")
	if err != nil {
		return 0, err
	}
	knows, err := loadEdges(ds, "KNOWS")
	if err != nil {
		return 0, err
	}
	likes, err := loadEdges(ds, "LIKES")
	if err != nil {
		return 0, err
	}
	containerOf, err := loadEdges(ds, "CONTAINER_OF")
	if err != nil {
		return 0, err
	}
	creators := groupStarts(hasCreator)
	knowsOut := groupStarts(knows)
	knowsDeg := startDegree(knows)
	likesIn := endDegree(likes)
	containerIn := endDegree(containerOf)

	// twoChain[p] is the number of KNOWS 2-paths out of p, cached so a person
	// who created many posts is walked once.
	twoChain := map[string]int64{}
	chainFor := func(p string) int64 {
		if v, ok := twoChain[p]; ok {
			return v
		}
		var c int64
		for _, q := range knowsOut[p] {
			c += knowsDeg[q]
		}
		twoChain[p] = c
		return c
	}

	var count int64
	for m, ps := range creators {
		fan := likesIn[m] * containerIn[m]
		if fan == 0 {
			continue
		}
		var chain int64
		for _, p := range ps {
			chain += chainFor(p)
		}
		count += chain * fan
	}
	return count, nil
}

// q5Count: the undirected KNOWS triangle (a)-[:KNOWS]-(b)-[:KNOWS]-(c)
// -[:KNOWS]-(a). Each distinct node triangle is matched 3! = 6 times, and
// each match picks one concrete relationship per unordered pair, so the
// count is six times the sum over triangles of the pairwise edge
// multiplicities' product.
func q5Count(ds engine.Dataset) (int64, error) {
	knows, err := loadEdges(ds, "KNOWS")
	if err != nil {
		return 0, err
	}
	adj, mult := undirectedAdjacency(knows)
	var sum int64
	forEachTriangle(adj, func(a, b, c string) {
		sum += mult[pairKey(a, b)] * mult[pairKey(b, c)] * mult[pairKey(a, c)]
	})
	return 6 * sum, nil
}

// q6Count: a KNOWS triangle whose members each created a post contained in a
// shared forum f. Inside a triangle the three posts are forced distinct
// (single creator per post), so for a fixed forum the matches multiply:
// k[p][f] is the number of (creator, containment) pairs putting a post of p
// in f, and the per-triangle factor is the sum over forums of the product.
func q6Count(ds engine.Dataset) (int64, error) {
	knows, err := loadEdges(ds, "KNOWS")
	if err != nil {
		return 0, err
	}
	k, err := postsPerPersonForum(ds)
	if err != nil {
		return 0, err
	}
	adj, mult := undirectedAdjacency(knows)
	var sum int64
	forEachTriangle(adj, func(a, b, c string) {
		shared := sharedProduct(k, a, b, c)
		if shared == 0 {
			return
		}
		sum += mult[pairKey(a, b)] * mult[pairKey(b, c)] * mult[pairKey(a, c)] * shared
	})
	return 6 * sum, nil
}

// q7Count: the undirected KNOWS four-cycle (a)-[:KNOWS]-(b)-[:KNOWS]-(c)
// -[:KNOWS]-(d)-[:KNOWS]-(a). Counted by direct enumeration over concrete
// relationships with the pairwise-distinctness Cypher requires, which gets
// the degenerate matches right (a pair known in both directions supports a
// two-pair "cycle" through two distinct relationships) without a simple-graph
// assumption. Each simple four-cycle is found eight times (four starting
// nodes, two directions), matching count(*).
func q7Count(ds engine.Dataset) (int64, error) {
	knows, err := loadEdges(ds, "KNOWS")
	if err != nil {
		return 0, err
	}
	return fourCycleMatches(knows), nil
}

// q8Count: the shared substructure: (p:Person)<-[:HAS_CREATOR]-(m1:Post)
// <-[:CONTAINER_OF]-(f:Forum), (p)<-[:HAS_CREATOR]-(m2:Post)
// <-[:CONTAINER_OF]-(f), m1 <> m2. For a fixed person and forum with k
// qualifying posts the ordered distinct pairs number k*(k-1); the count is
// that sum over all (person, forum) pairs.
func q8Count(ds engine.Dataset) (int64, error) {
	k, err := postsPerPersonForum(ds)
	if err != nil {
		return 0, err
	}
	var count int64
	for _, byForum := range k {
		for _, n := range byForum {
			count += n * (n - 1)
		}
	}
	return count, nil
}

// q9Count: a KNOWS triangle whose members share a forum (each a HAS_MEMBER
// target) and a post all three liked. The forum and the post are independent
// given the triangle, so the per-triangle factor is the product of the
// common-forum and common-liked-post counts, each summed with edge
// multiplicities.
func q9Count(ds engine.Dataset) (int64, error) {
	knows, err := loadEdges(ds, "KNOWS")
	if err != nil {
		return 0, err
	}
	hasMember, err := loadEdges(ds, "HAS_MEMBER")
	if err != nil {
		return 0, err
	}
	likes, err := loadEdges(ds, "LIKES")
	if err != nil {
		return 0, err
	}
	// memberOf[person][forum] and liked[person][post] carry per-pair edge
	// counts (0/1 from the generator, but counted, not assumed).
	memberOf := groupCountsByEnd(hasMember)
	liked := groupCountsByStart(likes)

	adj, mult := undirectedAdjacency(knows)
	var sum int64
	forEachTriangle(adj, func(a, b, c string) {
		forums := sharedProduct(memberOf, a, b, c)
		if forums == 0 {
			return
		}
		posts := sharedProduct(liked, a, b, c)
		if posts == 0 {
			return
		}
		sum += mult[pairKey(a, b)] * mult[pairKey(b, c)] * mult[pairKey(a, c)] * forums * posts
	})
	return 6 * sum, nil
}

// ---- shared structure builders --------------------------------------

// postsPerPersonForum builds k[person][forum]: the number of (HAS_CREATOR,
// CONTAINER_OF) pairs putting a post created by the person into the forum.
func postsPerPersonForum(ds engine.Dataset) (map[string]map[string]int64, error) {
	hasCreator, err := loadEdges(ds, "HAS_CREATOR")
	if err != nil {
		return nil, err
	}
	containerOf, err := loadEdges(ds, "CONTAINER_OF")
	if err != nil {
		return nil, err
	}
	creators := groupStarts(hasCreator)
	k := map[string]map[string]int64{}
	for _, e := range containerOf {
		f, m := e[0], e[1]
		for _, p := range creators[m] {
			if k[p] == nil {
				k[p] = map[string]int64{}
			}
			k[p][f]++
		}
	}
	return k, nil
}

// undirectedAdjacency folds directed edges into an undirected adjacency set
// plus the per-unordered-pair relationship count (a pair connected in both
// directions has multiplicity two). Self-loops are dropped: no loopless
// pattern can use them.
func undirectedAdjacency(edges [][2]string) (map[string]map[string]struct{}, map[[2]string]int64) {
	adj := map[string]map[string]struct{}{}
	mult := map[[2]string]int64{}
	for _, e := range edges {
		u, v := e[0], e[1]
		if u == v {
			continue
		}
		if adj[u] == nil {
			adj[u] = map[string]struct{}{}
		}
		if adj[v] == nil {
			adj[v] = map[string]struct{}{}
		}
		adj[u][v] = struct{}{}
		adj[v][u] = struct{}{}
		mult[pairKey(u, v)]++
	}
	return adj, mult
}

// forEachTriangle calls fn once for each distinct undirected triangle in adj,
// with its three nodes in ascending id order: the forward-only enumeration,
// lifted into a callback so each oracle attaches its own per-triangle weight.
func forEachTriangle(adj map[string]map[string]struct{}, fn func(a, b, c string)) {
	for a, nbrs := range adj {
		for b := range nbrs {
			if b <= a {
				continue
			}
			for c := range nbrs {
				if c <= b {
					continue
				}
				if _, ok := adj[b][c]; ok {
					fn(a, b, c)
				}
			}
		}
	}
}

// fourCycleMatches returns count(*) for the undirected four-cycle pattern by
// enumerating concrete relationship assignments: for every node a and every
// walk e1,e2,e3 over pairwise-distinct relationships reaching d, the closing
// candidates are the relationships between {d, a} not already used. Each
// match is one (node, relationship) assignment of the pattern, so the total
// is count(*) exactly.
func fourCycleMatches(edges [][2]string) int64 {
	type arc struct {
		rel   int
		other string
	}
	inc := map[string][]arc{}
	mult := map[[2]string]int64{}
	for i, e := range edges {
		u, v := e[0], e[1]
		if u == v {
			continue
		}
		inc[u] = append(inc[u], arc{i, v})
		inc[v] = append(inc[v], arc{i, u})
		mult[pairKey(u, v)]++
	}
	// onPair reports whether relationship r connects the unordered pair {x,y}.
	onPair := func(r int, x, y string) bool {
		e := edges[r]
		return (e[0] == x && e[1] == y) || (e[0] == y && e[1] == x)
	}

	var count int64
	for a, arcsA := range inc {
		for _, e1 := range arcsA {
			b := e1.other
			for _, e2 := range inc[b] {
				if e2.rel == e1.rel {
					continue
				}
				c := e2.other
				if c == a {
					// b's neighbor folding straight back onto a: the closing
					// pattern node d would coincide with b only through a
					// relationship between {a, b} — handled below like any
					// other c, but c == a makes edge (c,d) share a's pair
					// space; keep it, Cypher allows the node coincidence.
					_ = c
				}
				for _, e3 := range inc[c] {
					if e3.rel == e1.rel || e3.rel == e2.rel {
						continue
					}
					d := e3.other
					// Closing relationships between {d, a}, minus the ones
					// already used by e1..e3.
					m := mult[pairKey(d, a)]
					if m == 0 {
						continue
					}
					var used int64
					if onPair(e1.rel, d, a) {
						used++
					}
					if onPair(e2.rel, d, a) {
						used++
					}
					if onPair(e3.rel, d, a) {
						used++
					}
					count += m - used
				}
			}
		}
	}
	return count
}

// sharedProduct sums k[a][x]*k[b][x]*k[c][x] over the keys x the three
// persons share, iterating the smallest of the three maps so the cost scales
// with the rarest person's key count.
func sharedProduct(k map[string]map[string]int64, a, b, c string) int64 {
	ka, kb, kc := k[a], k[b], k[c]
	if len(ka) == 0 || len(kb) == 0 || len(kc) == 0 {
		return 0
	}
	smallest := ka
	if len(kb) < len(smallest) {
		smallest = kb
	}
	if len(kc) < len(smallest) {
		smallest = kc
	}
	var sum int64
	for x := range smallest {
		na, ok := ka[x]
		if !ok {
			continue
		}
		nb, ok := kb[x]
		if !ok {
			continue
		}
		nc, ok := kc[x]
		if !ok {
			continue
		}
		sum += na * nb * nc
	}
	return sum
}

// ---- edge loading and grouping --------------------------------------

// loadEdges reads every CSV shard for a relationship type and returns the
// concatenated [start, end] pairs, locating the endpoint columns through the
// typed header (spec 05 §1). An unknown type is an error from RelFiles; a
// type with no files is an empty slice (a pattern over an absent type counts
// zero).
func loadEdges(ds engine.Dataset, typ string) ([][2]string, error) {
	files, err := ds.RelFiles(typ)
	if err != nil {
		return nil, fmt.Errorf("lsqb: %s files: %w", typ, err)
	}
	var all [][2]string
	for _, f := range files {
		pairs, err := readCSVEdges(f)
		if err != nil {
			return nil, fmt.Errorf("lsqb: read %s: %w", f, err)
		}
		all = append(all, pairs...)
	}
	return all, nil
}

// readCSVEdges reads the :START_ID and :END_ID columns of one relationship
// CSV.
func readCSVEdges(path string) ([][2]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.ReuseRecord = true
	startCol, endCol := -1, -1
	first := true
	var pairs [][2]string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return pairs, nil
		}
		if err != nil {
			return nil, err
		}
		if first {
			first = false
			for i, cell := range rec {
				switch {
				case strings.HasSuffix(cell, ":START_ID"):
					startCol = i
				case strings.HasSuffix(cell, ":END_ID"):
					endCol = i
				}
			}
			if startCol < 0 || endCol < 0 {
				return nil, fmt.Errorf("header has no :START_ID/:END_ID columns")
			}
			continue
		}
		if startCol >= len(rec) || endCol >= len(rec) {
			return nil, fmt.Errorf("row has %d fields, need endpoints at %d,%d", len(rec), startCol, endCol)
		}
		pairs = append(pairs, [2]string{rec[startCol], rec[endCol]})
	}
}

// groupStarts buckets edges by start id: g[start] is the list of end ids.
func groupStarts(edges [][2]string) map[string][]string {
	g := map[string][]string{}
	for _, e := range edges {
		g[e[0]] = append(g[e[0]], e[1])
	}
	return g
}

// startDegree counts edges by start id.
func startDegree(edges [][2]string) map[string]int64 {
	d := map[string]int64{}
	for _, e := range edges {
		d[e[0]]++
	}
	return d
}

// endDegree counts edges by end id.
func endDegree(edges [][2]string) map[string]int64 {
	d := map[string]int64{}
	for _, e := range edges {
		d[e[1]]++
	}
	return d
}

// pairCounts counts edges per ordered (start, end) pair.
func pairCounts(edges [][2]string) map[[2]string]int64 {
	c := map[[2]string]int64{}
	for _, e := range edges {
		c[arcKey(e[0], e[1])]++
	}
	return c
}

// arcKey returns the directed (start, end) key pairCounts stores under, as
// distinct from pairKey's canonical unordered key.
//
// The two key spaces coincide whenever start sorts before end, which is why
// mixing them hid for so long: while ids carried a label prefix, a Forum id
// ("F0") always sorted before a Person id ("P3"), so a canonical lookup of a
// directed pair happened to find it. Under canonical integer ids the label
// blocks sort by number, the accident stops holding, and a canonical lookup
// silently misses most of the map — an undercount, never an error.
func arcKey(start, end string) [2]string { return [2]string{start, end} }

// groupCountsByStart builds m[start][end] = edge count.
func groupCountsByStart(edges [][2]string) map[string]map[string]int64 {
	m := map[string]map[string]int64{}
	for _, e := range edges {
		if m[e[0]] == nil {
			m[e[0]] = map[string]int64{}
		}
		m[e[0]][e[1]]++
	}
	return m
}

// groupCountsByEnd builds m[end][start] = edge count (HAS_MEMBER is
// Forum->Person, read person-first).
func groupCountsByEnd(edges [][2]string) map[string]map[string]int64 {
	m := map[string]map[string]int64{}
	for _, e := range edges {
		if m[e[1]] == nil {
			m[e[1]] = map[string]int64{}
		}
		m[e[1]][e[0]]++
	}
	return m
}

// pairKey returns the canonical unordered-pair key.
func pairKey(u, v string) [2]string {
	if u > v {
		u, v = v, u
	}
	return [2]string{u, v}
}
