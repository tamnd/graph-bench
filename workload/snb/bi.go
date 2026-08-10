package snb

import (
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

// This file registers "snb-bi": a BI-shaped aggregation subset of LDBC SNB
// Business Intelligence on the social schema (spec 07 §2). Analytics is
// true: single-stream repetitions under the analytics protocol, no
// concurrency sweeps. The runner computes the power score (the geometric
// mean of per-query times, the spec's shape) from the measure output for
// any Analytics workload; this package only marks the workload, it does not
// compute the score.
//
// Included shapes: BI1 (posting summary by year and length bucket), BI4
// (top forums by member count), BI5 (most active posters of a forum), BI9
// (substituted, below), BI18 (friend recommendation by mutual friends).
// Substitution: BI9 ranks top post creators among a forum's members — the
// official query walks moderators and their friends, and the schema has no
// moderators. Also disclosed: the social generator emits fixed-length
// (32-char) post content, so BI1's length-bucket dimension is degenerate on
// generated data (every post lands in one bucket); the year dimension
// carries the variation, and the query keeps the spec's group-by shape.

func init() {
	workload.Register(&workload.Workload{
		Name:      "snb-bi",
		Title:     "SNB BI aggregation subset BI1/4/5/9/18 (shapes, social schema)",
		Family:    "snb",
		Dataset:   "social-1k",
		Fidelity:  "derived",
		Analytics: true,
		Queries:   biQueries,
	})
}

var biQueries = []*workload.Query{qBI1, qBI4, qBI5, qBI9, qBI18}

// biYearSeconds buckets creationDate (seconds into the generator's
// five-year window) into years, mirroring the integer division in the
// query texts.
const biYearSeconds = int64(365 * 86400)

// qBI1 — BI1 shape: posting summary grouped by year and content-length
// bucket.
var qBI1 = &workload.Query{
	ID:    "snb-bi1",
	Class: engine.Aggregation,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (m:Post)
WITH m.creationDate / 31536000 AS year,
     CASE WHEN size(m.content) < 16 THEN 0
          WHEN size(m.content) < 32 THEN 1
          ELSE 2 END AS lengthBucket
RETURN year, lengthBucket, count(*) AS postCount
ORDER BY year ASC, lengthBucket ASC`,
	},
	Params: workload.Fixed{P: workload.Params{}},
	Reference: &workload.RefStrategy{
		Compare: workload.CompareSpec{Ordered: true},
		Compute: func(ds engine.Dataset, _ workload.Params) (*workload.Answer, error) {
			m, err := loadSocial(ds)
			if err != nil {
				return nil, err
			}
			type group struct{ year, bucket int64 }
			counts := map[group]int64{}
			for _, po := range m.posts {
				g := group{year: po.creationDate / biYearSeconds, bucket: 2}
				switch n := len(po.content); {
				case n < 16:
					g.bucket = 0
				case n < 32:
					g.bucket = 1
				}
				counts[g]++
			}
			var rows [][]engine.Value
			for g, n := range counts {
				rows = append(rows, row(g.year, g.bucket, n))
			}
			sortRows(rows, sortKey{col: 0}, sortKey{col: 1})
			return &workload.Answer{
				Columns: []string{"year", "lengthBucket", "postCount"},
				Rows:    rows,
			}, nil
		},
	},
}

// qBI4 — BI4 shape: top forums by member count.
var qBI4 = &workload.Query{
	ID:    "snb-bi4",
	Class: engine.Aggregation,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (f:Forum)-[:HAS_MEMBER]->(p:Person)
RETURN f.id AS forumId, f.title AS title, count(p) AS members
ORDER BY members DESC, forumId ASC
LIMIT 20`,
	},
	Params: workload.Fixed{P: workload.Params{}},
	Reference: &workload.RefStrategy{
		Compare: workload.CompareSpec{Ordered: true},
		Compute: func(ds engine.Dataset, _ workload.Params) (*workload.Answer, error) {
			m, err := loadSocial(ds)
			if err != nil {
				return nil, err
			}
			var rows [][]engine.Value
			for fi, members := range m.forumMembers {
				if len(members) == 0 {
					continue
				}
				rows = append(rows, row(idv(m.forums[fi].id), m.forums[fi].title, int64(len(members))))
			}
			sortRows(rows, sortKey{col: 2, desc: true}, sortKey{col: 0})
			return &workload.Answer{
				Columns: []string{"forumId", "title", "members"},
				Rows:    limit(rows, 20),
			}, nil
		},
	},
}

// qBI5 — BI5 shape: the most active post creators within one forum.
var qBI5 = &workload.Query{
	ID:    "snb-bi5",
	Class: engine.Aggregation,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (f:Forum {id: $forumId})-[:CONTAINER_OF]->(m:Post)-[:HAS_CREATOR]->(p:Person)
RETURN p.id AS personId, count(m) AS postCount
ORDER BY postCount DESC, personId ASC
LIMIT 20`,
	},
	PoolKey: PoolForumID,
	Params:  workload.NewPoolSource(nil),
	Reference: &workload.RefStrategy{
		Compare: workload.CompareSpec{Ordered: true},
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			m, err := loadSocial(ds)
			if err != nil {
				return nil, err
			}
			id, err := pstr(p, "forumId")
			if err != nil {
				return nil, err
			}
			fi, err := m.forum(id)
			if err != nil {
				return nil, err
			}
			counts := map[int]int64{}
			for _, pi := range m.forumPosts[fi] {
				counts[m.postCreator[pi]]++
			}
			var rows [][]engine.Value
			for ci, n := range counts {
				rows = append(rows, row(idv(m.persons[ci].id), n))
			}
			sortRows(rows, sortKey{col: 1, desc: true}, sortKey{col: 0})
			return &workload.Answer{
				Columns: []string{"personId", "postCount"},
				Rows:    limit(rows, 20),
			}, nil
		},
	},
}

// qBI9 — BI9 substitute (disclosed above): top post creators among one
// forum's members.
var qBI9 = &workload.Query{
	ID:    "snb-bi9",
	Class: engine.Aggregation,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (f:Forum {id: $forumId})-[:HAS_MEMBER]->(p:Person)<-[:HAS_CREATOR]-(m:Post)
RETURN p.id AS personId, p.firstName AS firstName, count(m) AS postCount
ORDER BY postCount DESC, personId ASC
LIMIT 20`,
	},
	PoolKey: PoolForumID,
	Params:  workload.NewPoolSource(nil),
	Reference: &workload.RefStrategy{
		Compare: workload.CompareSpec{Ordered: true},
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			m, err := loadSocial(ds)
			if err != nil {
				return nil, err
			}
			id, err := pstr(p, "forumId")
			if err != nil {
				return nil, err
			}
			fi, err := m.forum(id)
			if err != nil {
				return nil, err
			}
			var rows [][]engine.Value
			for _, pm := range m.forumMembers[fi] {
				if len(m.postsOf[pm]) == 0 {
					continue
				}
				pr := m.persons[pm]
				rows = append(rows, row(idv(pr.id), pr.firstName, int64(len(m.postsOf[pm]))))
			}
			sortRows(rows, sortKey{col: 2, desc: true}, sortKey{col: 0})
			return &workload.Answer{
				Columns: []string{"personId", "firstName", "postCount"},
				Rows:    limit(rows, 20),
			}, nil
		},
	},
}

// qBI18 — BI18 shape: friend recommendation — persons the parameter person
// does not know, ranked by mutual friend count.
//
// The anti-join is spelled by collecting the friend ids and testing
// membership, rather than the more idiomatic OPTIONAL MATCH + count(k) = 0 or
// an exists{} subquery. The subquery form has no Kùzu spelling at all, and
// the OPTIONAL MATCH form is worse than unsupported: Kùzu accepts it and
// returns a different answer, counting zero for an optional relationship
// pattern that does match, so every direct friend survives the anti-join and
// the recommendation list fills with people the person already knows. That is
// a wrong answer no reader would suspect from the query text, which is
// exactly the failure a portable spelling has to avoid.
var qBI18 = &workload.Query{
	ID:    "snb-bi18",
	Class: engine.Aggregation,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (p:Person {id: $personId})-[:KNOWS]-(f:Person)
WITH collect(DISTINCT f.id) AS friendIds
UNWIND friendIds AS fid
MATCH (:Person {id: fid})-[:KNOWS]-(c:Person)
WHERE c.id <> $personId AND NOT c.id IN friendIds
WITH c.id AS personId, count(DISTINCT fid) AS mutualFriends
RETURN personId, mutualFriends
ORDER BY mutualFriends DESC, personId ASC
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
			// mutual[c] = the set of distinct friends of i adjacent to c,
			// for candidates c that are neither i nor already friends.
			mutual := map[int]map[int]struct{}{}
			for _, f := range m.friends[i] {
				for _, c := range m.friends[f] {
					if c == i {
						continue
					}
					if _, direct := inFriends[c]; direct {
						continue
					}
					if mutual[c] == nil {
						mutual[c] = map[int]struct{}{}
					}
					mutual[c][f] = struct{}{}
				}
			}
			var rows [][]engine.Value
			for c, fs := range mutual {
				rows = append(rows, row(idv(m.persons[c].id), int64(len(fs))))
			}
			sortRows(rows, sortKey{col: 1, desc: true}, sortKey{col: 0})
			return &workload.Answer{
				Columns: []string{"personId", "mutualFriends"},
				Rows:    limit(rows, 20),
			}, nil
		},
	},
}
