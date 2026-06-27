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
// engine-independent method over the canonical CSV. It covers every LSQB query:
// the tree-shaped joins (Q1-Q4), the cyclic and shared-substructure patterns the
// spec singles out for an independent subgraph-counting reference
// (notes/Spec/2060/bench section 2.5: Q5 the 3-clique, Q7 the four-cycle, Q8 the
// shared substructure), and the dense composite cycles (Q6, Q9).
//
// The returned value is count(*) under Cypher relationship-isomorphism, the same
// quantity the engine returns: relationships in a pattern must be pairwise
// distinct, but nodes may coincide (gr matches under relationship-uniqueness,
// not node-uniqueness). That semantics decides the automorphism multiplicity. An
// undirected triangle is matched six times (3! node orderings over a symmetric
// pattern), so Q5, Q6, and Q9 are six times their distinct-triangle sum; the
// tree joins Q1-Q4 have no symmetry and no repeated relationship, so their
// match count is the plain product of the independent fan-outs at each branch.
//
// Ambiguous relationship types (IS_LOCATED_IN is used by Person->City and by
// Message->Country; HAS_TAG by Message->Tag and Forum->Tag) are read by bare
// type without a label filter, the same convention triangleCount and the Q8
// oracle already use. That is sound here because LDBC assigns globally unique
// node ids and every ambiguous type is entered only through a join that pins the
// endpoint's label: a person reached through STUDY_AT or HAS_CREATOR has only
// City located-in edges under its id, and a message reached through HAS_CREATOR
// has only its own (never a forum's) tag edges. The implementation note in
// notes/Spec/2060/bench/implementation carries the full argument.
//
// Verified against brute-force pattern enumeration on hand-built graphs (see
// oracle_test.go) and, for the cyclic trio, against gr: a square with one chord
// gives Q5 = 12, Q7 = 8, and the two-message fixture gives Q8 = 2.
func CountOracle(queryID string, ds target.Dataset) (int64, error) {
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
		// triangleCount returns distinct triangles; count(*) over the symmetric
		// three-relationship pattern matches each one 3! = 6 times.
		n, err := triangleCount(ds)
		if err != nil {
			return 0, err
		}
		return 6 * n, nil
	case "lsqb-q6":
		return q6Count(ds)
	case "lsqb-q7":
		return fourCycleCount(ds)
	case "lsqb-q8":
		return sharedSubstructureCount(ds)
	case "lsqb-q9":
		return q9Count(ds)
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

// loadEdges reads every CSV shard for a relationship type and returns the
// concatenated [start, end] pairs. An unknown type or an unreadable file is an
// error; a type with no files is an empty slice (a pattern over an absent type
// then counts zero).
func loadEdges(ds target.Dataset, typ string) ([][2]string, error) {
	files, _, err := ds.RelFiles(typ)
	if err != nil {
		return nil, fmt.Errorf("lsqb: %s files: %w", typ, err)
	}
	var all [][2]string
	for _, f := range files {
		edges, err := readCSVEdges(f)
		if err != nil {
			return nil, fmt.Errorf("lsqb: read %s: %w", f, err)
		}
		all = append(all, edges...)
	}
	return all, nil
}

// groupStarts buckets edges by start id, so g[start] is the list of end ids. It
// is the adjacency list of a directed relationship type, used to walk a fan-out
// from a known node (a forum's messages, a message's tags).
func groupStarts(edges [][2]string) map[string][]string {
	g := map[string][]string{}
	for _, e := range edges {
		g[e[0]] = append(g[e[0]], e[1])
	}
	return g
}

// startDegree counts edges by start id, so d[start] is the out-degree. It is the
// length of groupStarts(edges)[start] without materializing the lists, for the
// branches that only need the fan-out size (a person's universities, a tag's
// classes).
func startDegree(edges [][2]string) map[string]int64 {
	d := map[string]int64{}
	for _, e := range edges {
		d[e[0]]++
	}
	return d
}

// q1Count is the oracle for Q1: (p:Person)-[:IS_LOCATED_IN]->(:City)
// -[:IS_PART_OF]->(:Country) together with (p)-[:STUDY_AT]->(:University). The
// two branches out of p are independent, so for each person the match count is
// the product of the located-in/part-of chain length and the study-at degree.
//
// Let partOfDeg[city] be the number of countries a city is part of (one in a
// clean SNB, but counted, not assumed). For a person p the chain length is the
// sum of partOfDeg[city] over p's cities. studyAt pins p to a Person, so the sum
// runs over persons that study somewhere; a person with no university contributes
// a zero product and is skipped.
func q1Count(ds target.Dataset) (int64, error) {
	locatedIn, err := loadEdges(ds, "IS_LOCATED_IN")
	if err != nil {
		return 0, err
	}
	partOf, err := loadEdges(ds, "IS_PART_OF")
	if err != nil {
		return 0, err
	}
	studyAt, err := loadEdges(ds, "STUDY_AT")
	if err != nil {
		return 0, err
	}
	cities := groupStarts(locatedIn)
	partOfDeg := startDegree(partOf)
	studyAtDeg := startDegree(studyAt)

	var count int64
	for p, deg := range studyAtDeg {
		var chain int64
		for _, city := range cities[p] {
			chain += partOfDeg[city]
		}
		count += chain * deg
	}
	return count, nil
}

// q2Count is the oracle for Q2: (m:Message)-[:HAS_CREATOR]->(p:Person)
// -[:IS_LOCATED_IN]->(:City) together with (m)-[:HAS_TAG]->(:Tag). The message is
// the join hub with two independent fan-outs: the creator-to-city chain and the
// message's tags. For each message the match count is the product.
//
// HAS_CREATOR pins m to a Message and p to a Person, so reading IS_LOCATED_IN and
// HAS_TAG by bare type is safe: locatedInDeg[p] for a real person counts only its
// city edges, and hasTagDeg[m] for a real message counts only its own tag edges.
func q2Count(ds target.Dataset) (int64, error) {
	hasCreator, err := loadEdges(ds, "HAS_CREATOR")
	if err != nil {
		return 0, err
	}
	locatedIn, err := loadEdges(ds, "IS_LOCATED_IN")
	if err != nil {
		return 0, err
	}
	hasTag, err := loadEdges(ds, "HAS_TAG")
	if err != nil {
		return 0, err
	}
	creators := groupStarts(hasCreator)
	locatedInDeg := startDegree(locatedIn)
	hasTagDeg := startDegree(hasTag)

	var count int64
	for m, ps := range creators {
		var cityChain int64
		for _, p := range ps {
			cityChain += locatedInDeg[p]
		}
		count += cityChain * hasTagDeg[m]
	}
	return count, nil
}

// q3Count is the oracle for Q3: (f:Forum)-[:HAS_MODERATOR]->(:Person),
// (f)-[:HAS_MEMBER]->(p:Person), (f)-[:CONTAINER_OF]->(m:Message)-[:HAS_CREATOR]->(p).
// The bound person p is shared between the member edge and the message's creator,
// so this is not a pure tree; the moderator is an independent fan-out.
//
// Under relationship-uniqueness the moderator may coincide with p, so it
// contributes a plain factor: for each forum the count is the moderator degree
// times the number of (message, person) pairs where the forum contains the
// message, the message's creator is a forum member. So for each forum, sum over
// its contained messages the count of creators that are also members.
func q3Count(ds target.Dataset) (int64, error) {
	hasModerator, err := loadEdges(ds, "HAS_MODERATOR")
	if err != nil {
		return 0, err
	}
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
	modDeg := startDegree(hasModerator)
	members := map[string]map[string]struct{}{}
	for _, e := range hasMember {
		if members[e[0]] == nil {
			members[e[0]] = map[string]struct{}{}
		}
		members[e[0]][e[1]] = struct{}{}
	}
	contains := groupStarts(containerOf)
	creators := groupStarts(hasCreator)

	var count int64
	for f, deg := range modDeg {
		memberSet := members[f]
		if len(memberSet) == 0 {
			continue
		}
		var pairs int64
		for _, m := range contains[f] {
			for _, p := range creators[m] {
				if _, ok := memberSet[p]; ok {
					pairs++
				}
			}
		}
		count += deg * pairs
	}
	return count, nil
}

// q4Count is the oracle for Q4: (m:Message)-[:HAS_TAG]->(:Tag)-[:HAS_TYPE]->
// (:TagClass) together with (m)-[:HAS_CREATOR]->(:Person)-[:IS_LOCATED_IN]->
// (:City)-[:IS_PART_OF]->(:Country). The message is the hub with two independent
// chains: tag-to-class and creator-to-city-to-country. For each message the
// match count is the product of the two chain lengths.
//
// A(m) is the tag-class chain: sum over the message's tags of the tag's type
// degree. B(m) is the location chain: sum over the message's creators of, for
// each creator's city, the city's part-of degree. HAS_CREATOR pins m to a
// Message and the creator to a Person, so the bare-type reads of HAS_TAG and
// IS_LOCATED_IN stay label-correct.
func q4Count(ds target.Dataset) (int64, error) {
	hasTag, err := loadEdges(ds, "HAS_TAG")
	if err != nil {
		return 0, err
	}
	hasType, err := loadEdges(ds, "HAS_TYPE")
	if err != nil {
		return 0, err
	}
	hasCreator, err := loadEdges(ds, "HAS_CREATOR")
	if err != nil {
		return 0, err
	}
	locatedIn, err := loadEdges(ds, "IS_LOCATED_IN")
	if err != nil {
		return 0, err
	}
	partOf, err := loadEdges(ds, "IS_PART_OF")
	if err != nil {
		return 0, err
	}
	tagsOf := groupStarts(hasTag)
	hasTypeDeg := startDegree(hasType)
	creators := groupStarts(hasCreator)
	cities := groupStarts(locatedIn)
	partOfDeg := startDegree(partOf)

	// locationChain[p] is the city-to-country chain length for a person, cached
	// so a person who created many messages is walked once.
	locationChain := map[string]int64{}
	chainFor := func(p string) int64 {
		if v, ok := locationChain[p]; ok {
			return v
		}
		var c int64
		for _, city := range cities[p] {
			c += partOfDeg[city]
		}
		locationChain[p] = c
		return c
	}

	var count int64
	for m, tags := range tagsOf {
		var a int64
		for _, t := range tags {
			a += hasTypeDeg[t]
		}
		if a == 0 {
			continue
		}
		var b int64
		for _, p := range creators[m] {
			b += chainFor(p)
		}
		count += a * b
	}
	return count, nil
}

// q6Count is the oracle for Q6: a KNOWS triangle (a,b,c) where each person
// created a message carrying a shared tag t. The triangle is symmetric over its
// three KNOWS relationships, so count(*) is six times the sum over distinct
// triangles. Inside a triangle the three messages are forced distinct (different
// creators) and the only shared node is the tag, so for a fixed t the number of
// matches is k[a][t]*k[b][t]*k[c][t], where k[person][tag] is the number of
// messages that person created carrying that tag. Summing over shared tags and
// distinct triangles, then multiplying by six, gives count(*).
func q6Count(ds target.Dataset) (int64, error) {
	adj, k, err := knowsAndCreatorTags(ds)
	if err != nil {
		return 0, err
	}
	var sum int64
	forEachTriangle(adj, func(a, b, c string) {
		sum += sharedTagProduct(k, a, b, c)
	})
	return 6 * sum, nil
}

// q9Count is the oracle for Q9: a KNOWS triangle (a,b,c) whose three members
// share a forum (each joined by HAS_MEMBER) and a tag (each created a message
// carrying it). The forum and the tag are independent given the triangle, so for
// a distinct triangle the match count is F*(shared-tag product), where F is the
// number of forums that have all three as members. As in Q6 the triangle is
// six-fold symmetric, so count(*) is six times the sum of F times the shared-tag
// product over distinct triangles.
func q9Count(ds target.Dataset) (int64, error) {
	adj, k, err := knowsAndCreatorTags(ds)
	if err != nil {
		return 0, err
	}
	hasMember, err := loadEdges(ds, "HAS_MEMBER")
	if err != nil {
		return 0, err
	}
	// memberForums[person] is the set of forums the person belongs to (HAS_MEMBER
	// is Forum->Person, so it is read end-to-start).
	memberForums := map[string]map[string]struct{}{}
	for _, e := range hasMember {
		f, p := e[0], e[1]
		if memberForums[p] == nil {
			memberForums[p] = map[string]struct{}{}
		}
		memberForums[p][f] = struct{}{}
	}
	var sum int64
	forEachTriangle(adj, func(a, b, c string) {
		shared := sharedTagProduct(k, a, b, c)
		if shared == 0 {
			return
		}
		sum += commonForums(memberForums, a, b, c) * shared
	})
	return 6 * sum, nil
}

// knowsAndCreatorTags builds the two structures the composite cyclic oracles
// share: the undirected KNOWS adjacency set and k[person][tag], the count of
// messages a person created carrying a tag. k is built by walking each message's
// creator and tags, so it never reads a forum's tags (a forum is not a creator).
func knowsAndCreatorTags(ds target.Dataset) (map[string]map[string]struct{}, map[string]map[string]int64, error) {
	knowsFiles, _, err := ds.RelFiles("KNOWS")
	if err != nil {
		return nil, nil, fmt.Errorf("lsqb: KNOWS files: %w", err)
	}
	adj, err := buildAdjacencySet(knowsFiles)
	if err != nil {
		return nil, nil, err
	}
	hasCreator, err := loadEdges(ds, "HAS_CREATOR")
	if err != nil {
		return nil, nil, err
	}
	hasTag, err := loadEdges(ds, "HAS_TAG")
	if err != nil {
		return nil, nil, err
	}
	creator := map[string]string{}
	for _, e := range hasCreator {
		creator[e[0]] = e[1] // message -> person
	}
	tagsOf := groupStarts(hasTag)
	k := map[string]map[string]int64{}
	for m, p := range creator {
		for _, t := range tagsOf[m] {
			if k[p] == nil {
				k[p] = map[string]int64{}
			}
			k[p][t]++
		}
	}
	return adj, k, nil
}

// forEachTriangle calls fn once for each distinct undirected triangle in adj,
// with its three nodes in ascending id order. It is the forward-only enumeration
// triangleCount uses, lifted into a callback so the composite oracles can attach
// their own per-triangle weight.
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

// sharedTagProduct sums k[a][t]*k[b][t]*k[c][t] over tags t the three persons
// all carry. It iterates the smallest of the three tag maps so the cost scales
// with the rarest person's tag count, not the product.
func sharedTagProduct(k map[string]map[string]int64, a, b, c string) int64 {
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
	for t := range smallest {
		ca, ok := ka[t]
		if !ok {
			continue
		}
		cb, ok := kb[t]
		if !ok {
			continue
		}
		cc, ok := kc[t]
		if !ok {
			continue
		}
		sum += ca * cb * cc
	}
	return sum
}

// commonForums returns the number of forums that have all three persons as
// members, by intersecting the smallest membership set against the other two.
func commonForums(memberForums map[string]map[string]struct{}, a, b, c string) int64 {
	fa, fb, fc := memberForums[a], memberForums[b], memberForums[c]
	if len(fa) == 0 || len(fb) == 0 || len(fc) == 0 {
		return 0
	}
	smallest := fa
	if len(fb) < len(smallest) {
		smallest = fb
	}
	if len(fc) < len(smallest) {
		smallest = fc
	}
	var n int64
	for f := range smallest {
		if _, ok := fa[f]; !ok {
			continue
		}
		if _, ok := fb[f]; !ok {
			continue
		}
		if _, ok := fc[f]; !ok {
			continue
		}
		n++
	}
	return n
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
