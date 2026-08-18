package snb

import (
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

// This file registers "snb-complex": the faithful-shape subset of the LDBC
// SNB Interactive v2 complex reads expressible on the social schema
// (spec 06 §3.2). Included: IC1, IC2, IC4 (substituted), IC5, IC9, IC13.
//
// Omitted ICs and why (one line each; the schema has no Comment, Tag,
// Place, or Organisation entities — package doc):
//
//   - IC3  (friends in two countries): no Places.
//   - IC6  (co-occurring tags): no Tags.
//   - IC7  (recent likers with reply latency): latency needs Comments.
//   - IC8  (recent replies): no Comments/REPLY_OF.
//   - IC10 (friend recommendation by tag interest): no Tags/interests.
//   - IC11 (job referral): no Organisations.
//   - IC12 (expert search): no Comments or Tag hierarchy.
//   - IC14 (cheapest interaction path): weights derive from reply
//     interactions, which need Comments.
//
// Substitution: IC4 (new topics — tags in friends' recent posts) becomes
// "recent posts liked by friends" — the same friend-fanout-then-window-scan
// shape over the dated relationship the schema has (LIKES).
//
// IC1 deviates in one respect: LDBC orders by knows-distance first;
// computing the per-row minimum distance in portable Cypher would dominate
// the query, so snb-ic1 returns the distinct 1..3-hop matches ordered by
// (lastName, personId) only.

func init() {
	workload.Register(&workload.Workload{
		Name:     "snb-complex",
		Title:    "SNB Interactive complex reads IC1/2/4/5/9/13 (shapes, social schema)",
		Family:   "snb",
		Dataset:  "social-1k",
		Fidelity: "derived",
		Queries:  complexQueries,
	})
}

var complexQueries = []*workload.Query{qIC1, qIC2, qIC4, qIC5, qIC9, qIC13}

// qIC1 — IC1 shape: persons with a given first name within 3 KNOWS hops.
var qIC1 = &workload.Query{
	ID:    "snb-ic1",
	Class: engine.Subgraph,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (p:Person {id: $personId})-[:KNOWS*1..3]-(f:Person)
WHERE f.firstName = $firstName AND f.id <> $personId
RETURN DISTINCT f.id AS personId, f.lastName AS lastName
ORDER BY lastName ASC, personId ASC
LIMIT 20`,
		engine.ZuQL: `MATCH (p:Person {id: $personId})-[:KNOWS*1..3]-(f:Person)
WHERE f.firstName = $firstName AND f.id <> $personId
RETURN DISTINCT f.id AS personId, f.lastName AS lastName
ORDER BY lastName ASC, personId ASC
LIMIT 20`,
	},
	PoolKey: PoolPersonName,
	Params:  workload.NewPoolSource(nil),
	Reference: &workload.RefStrategy{
		Compare: workload.CompareSpec{Ordered: true},
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			m, err := loadSocial(ds)
			if err != nil {
				return nil, err
			}
			id, err := pstr(p, "personId")
			if err != nil {
				return nil, err
			}
			name, err := pstr(p, "firstName")
			if err != nil {
				return nil, err
			}
			i, err := m.person(id)
			if err != nil {
				return nil, err
			}
			dist := m.bfs(i, 3)
			var rows [][]engine.Value
			for j, d := range dist {
				if d >= 1 && d <= 3 && m.persons[j].firstName == name {
					rows = append(rows, row(idv(m.persons[j].id), m.persons[j].lastName))
				}
			}
			sortRows(rows, sortKey{col: 1}, sortKey{col: 0})
			return &workload.Answer{
				Columns: []string{"personId", "lastName"},
				Rows:    limit(rows, 20),
			}, nil
		},
	},
}

// qIC2 — IC2 shape: friends' recent posts before a date, newest first.
var qIC2 = &workload.Query{
	ID:    "snb-ic2",
	Class: engine.Subgraph,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (p:Person {id: $personId})-[:KNOWS]-(f:Person)
WITH DISTINCT f
MATCH (f)<-[:HAS_CREATOR]-(m:Post)
WHERE m.creationDate < $maxDate
RETURN f.id AS personId, f.firstName AS firstName, f.lastName AS lastName,
       m.id AS postId, m.content AS content, m.creationDate AS creationDate
ORDER BY creationDate DESC, postId ASC
LIMIT 20`,
		engine.ZuQL: `MATCH (p:Person {id: $personId})-[:KNOWS]-(f:Person)
WITH DISTINCT f
MATCH (f)<-[:HAS_CREATOR]-(m:Post)
WHERE m.creationDate < $maxDate
RETURN f.id AS personId, f.firstName AS firstName, f.lastName AS lastName,
       m.id AS postId, m.content AS content, m.creationDate AS creationDate
ORDER BY creationDate DESC, postId ASC
LIMIT 20`,
	},
	PoolKey: PoolPersonDate,
	Params:  workload.NewPoolSource(nil),
	Reference: &workload.RefStrategy{
		Compare: workload.CompareSpec{Ordered: true},
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			m, err := loadSocial(ds)
			if err != nil {
				return nil, err
			}
			id, err := pstr(p, "personId")
			if err != nil {
				return nil, err
			}
			maxDate, err := pint(p, "maxDate")
			if err != nil {
				return nil, err
			}
			i, err := m.person(id)
			if err != nil {
				return nil, err
			}
			var rows [][]engine.Value
			for _, f := range m.friends[i] {
				fr := m.persons[f]
				for _, pi := range m.postsOf[f] {
					po := m.posts[pi]
					if po.creationDate < maxDate {
						rows = append(rows, row(idv(fr.id), fr.firstName, fr.lastName, idv(po.id), po.content, po.creationDate))
					}
				}
			}
			sortRows(rows, sortKey{col: 5, desc: true}, sortKey{col: 3})
			return &workload.Answer{
				Columns: []string{"personId", "firstName", "lastName", "postId", "content", "creationDate"},
				Rows:    limit(rows, 20),
			}, nil
		},
	},
}

// qIC4 — IC4 substitute (disclosed above): distinct posts liked by friends
// with a like date in the window.
var qIC4 = &workload.Query{
	ID:    "snb-ic4",
	Class: engine.Subgraph,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (p:Person {id: $personId})-[:KNOWS]-(f:Person)
WITH DISTINCT f
MATCH (f)-[l:LIKES]->(m:Post)
WHERE l.creationDate >= $minDate
RETURN DISTINCT m.id AS postId, m.content AS content
ORDER BY postId ASC
LIMIT 20`,
		engine.ZuQL: `MATCH (p:Person {id: $personId})-[:KNOWS]-(f:Person)
WITH DISTINCT f
MATCH (f)-[l:LIKES]->(m:Post)
WHERE l.creationDate >= $minDate
RETURN DISTINCT m.id AS postId, m.content AS content
ORDER BY postId ASC
LIMIT 20`,
	},
	PoolKey: PoolPersonDate,
	Params:  workload.NewPoolSource(nil),
	Reference: &workload.RefStrategy{
		Compare: workload.CompareSpec{Ordered: true},
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			m, err := loadSocial(ds)
			if err != nil {
				return nil, err
			}
			id, err := pstr(p, "personId")
			if err != nil {
				return nil, err
			}
			minDate, err := pint(p, "minDate")
			if err != nil {
				return nil, err
			}
			i, err := m.person(id)
			if err != nil {
				return nil, err
			}
			seen := map[int]struct{}{}
			for _, f := range m.friends[i] {
				for _, e := range m.personLikes[f] {
					if e.date >= minDate {
						seen[e.other] = struct{}{}
					}
				}
			}
			var rows [][]engine.Value
			for pi := range seen {
				rows = append(rows, row(idv(m.posts[pi].id), m.posts[pi].content))
			}
			sortRows(rows, sortKey{col: 0})
			return &workload.Answer{
				Columns: []string{"postId", "content"},
				Rows:    limit(rows, 20),
			}, nil
		},
	},
}

// qIC5 — IC5 shape (new groups): forums the person's friends belong to,
// ranked by how many friends are members. The schema's HAS_MEMBER carries
// no join date, so the LDBC date filter is dropped (disclosed).
var qIC5 = &workload.Query{
	ID:    "snb-ic5",
	Class: engine.Subgraph,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (p:Person {id: $personId})-[:KNOWS]-(f:Person)
WITH DISTINCT f
MATCH (forum:Forum)-[:HAS_MEMBER]->(f)
RETURN forum.id AS forumId, forum.title AS title, count(f) AS memberFriends
ORDER BY memberFriends DESC, forumId ASC
LIMIT 20`,
		engine.ZuQL: `MATCH (p:Person {id: $personId})-[:KNOWS]-(f:Person)
WITH DISTINCT f
MATCH (forum:Forum)-[:HAS_MEMBER]->(f)
RETURN forum.id AS forumId, forum.title AS title, count(f) AS memberFriends
ORDER BY memberFriends DESC, forumId ASC
LIMIT 20`,
	},
	PoolKey: PoolPersonID,
	Params:  workload.NewPoolSource(nil),
	Reference: &workload.RefStrategy{
		Compare: workload.CompareSpec{Ordered: true},
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			m, err := loadSocial(ds)
			if err != nil {
				return nil, err
			}
			id, err := pstr(p, "personId")
			if err != nil {
				return nil, err
			}
			i, err := m.person(id)
			if err != nil {
				return nil, err
			}
			inFriends := map[int]struct{}{}
			for _, f := range m.friends[i] {
				inFriends[f] = struct{}{}
			}
			counts := map[int]int64{}
			for fi, members := range m.forumMembers {
				for _, pm := range members {
					if _, ok := inFriends[pm]; ok {
						counts[fi]++
					}
				}
			}
			var rows [][]engine.Value
			for fi, n := range counts {
				rows = append(rows, row(idv(m.forums[fi].id), m.forums[fi].title, n))
			}
			sortRows(rows, sortKey{col: 2, desc: true}, sortKey{col: 0})
			return &workload.Answer{
				Columns: []string{"forumId", "title", "memberFriends"},
				Rows:    limit(rows, 20),
			}, nil
		},
	},
}

// qIC9 — IC9 shape: recent posts of friends and friends-of-friends before a
// date, newest first.
var qIC9 = &workload.Query{
	ID:    "snb-ic9",
	Class: engine.Subgraph,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (p:Person {id: $personId})-[:KNOWS*1..2]-(f:Person)
WHERE f.id <> $personId
WITH DISTINCT f
MATCH (f)<-[:HAS_CREATOR]-(m:Post)
WHERE m.creationDate < $maxDate
RETURN f.id AS personId, m.id AS postId, m.creationDate AS creationDate
ORDER BY creationDate DESC, postId ASC
LIMIT 20`,
		engine.ZuQL: `MATCH (p:Person {id: $personId})-[:KNOWS*1..2]-(f:Person)
WHERE f.id <> $personId
WITH DISTINCT f
MATCH (f)<-[:HAS_CREATOR]-(m:Post)
WHERE m.creationDate < $maxDate
RETURN f.id AS personId, m.id AS postId, m.creationDate AS creationDate
ORDER BY creationDate DESC, postId ASC
LIMIT 20`,
	},
	PoolKey: PoolPersonDate,
	Params:  workload.NewPoolSource(nil),
	Reference: &workload.RefStrategy{
		Compare: workload.CompareSpec{Ordered: true},
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			m, err := loadSocial(ds)
			if err != nil {
				return nil, err
			}
			id, err := pstr(p, "personId")
			if err != nil {
				return nil, err
			}
			maxDate, err := pint(p, "maxDate")
			if err != nil {
				return nil, err
			}
			i, err := m.person(id)
			if err != nil {
				return nil, err
			}
			dist := m.bfs(i, 2)
			var rows [][]engine.Value
			for j, d := range dist {
				if d < 1 || d > 2 {
					continue
				}
				for _, pi := range m.postsOf[j] {
					po := m.posts[pi]
					if po.creationDate < maxDate {
						rows = append(rows, row(idv(m.persons[j].id), idv(po.id), po.creationDate))
					}
				}
			}
			sortRows(rows, sortKey{col: 2, desc: true}, sortKey{col: 1})
			return &workload.Answer{
				Columns: []string{"personId", "postId", "creationDate"},
				Rows:    limit(rows, 20),
			}, nil
		},
	},
}

// qIC13 — IC13 shape: shortest path length between two persons over KNOWS,
// treated undirected. Zero rows when no path exists. The Cypher text uses
// Neo4j-style shortestPath(); the Kùzu text uses its SHORTEST recursive-rel
// syntax (bounded at 30 hops, far beyond the dataset's diameter). The
// reference is a plain breadth-first search over the KNOWS-only subgraph
// loaded by this package (workload.LoadGraph merges all relationship types,
// so it cannot serve here).
var qIC13 = &workload.Query{
	ID:    "snb-ic13",
	Class: engine.Traversal,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH path = shortestPath((a:Person {id: $person1Id})-[:KNOWS*]-(b:Person {id: $person2Id}))
RETURN length(path) AS len`,
		engine.KuzuCy: `MATCH (a:Person {id: $person1Id})-[e:KNOWS* SHORTEST 1..30]-(b:Person {id: $person2Id})
RETURN length(e) AS len`,
		engine.ZuQL: `MATCH ANY SHORTEST (a:Person {id: $person1Id})-[e:KNOWS*]-(b:Person {id: $person2Id})
RETURN size(e) AS len`,
	},
	PoolKey: PoolPersonPair,
	Params:  workload.NewPoolSource(nil),
	Reference: &workload.RefStrategy{
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			m, err := loadSocial(ds)
			if err != nil {
				return nil, err
			}
			aID, err := pstr(p, "person1Id")
			if err != nil {
				return nil, err
			}
			bID, err := pstr(p, "person2Id")
			if err != nil {
				return nil, err
			}
			a, err := m.person(aID)
			if err != nil {
				return nil, err
			}
			b, err := m.person(bID)
			if err != nil {
				return nil, err
			}
			// Both texts match paths of length >= 1, so a pair with no
			// path — and the degenerate a == b pair, which the pool never
			// emits — yields zero rows.
			ans := &workload.Answer{Columns: []string{"len"}}
			if d := m.bfs(a, -1)[b]; d >= 1 {
				ans.Rows = append(ans.Rows, row(int64(d)))
			}
			return ans, nil
		},
	},
}
