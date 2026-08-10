package snb

import (
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

// This file registers "snb-short": the LDBC SNB Interactive v2 short reads
// IS1-IS7 adapted to the social schema (spec 06 §3.1). Adaptations, all
// forced by the schema (package doc):
//
//   - IS1 returns the profile fields the schema has (no gender, browser, or
//     city).
//   - IS2 returns the person's own posts; there are no reply chains to walk
//     back to a root post.
//   - IS6 returns the containing forum's id and title; there is no
//     moderator.
//   - IS7 (replies to a message) is SUBSTITUTED: the schema has no
//     Comment/REPLY_OF, so snb-is7 asks for the likers of a post ordered by
//     like date — the same shape (fan-in one hop from a message with a dated
//     edge, ordered newest-first) over the relationship that exists.
//
// Where LDBC fixes the result order the query carries the ORDER BY and the
// reference compares ordered (CompareSpec{Ordered: true}).

func init() {
	workload.Register(&workload.Workload{
		Name:     "snb-short",
		Title:    "SNB Interactive short reads IS1-IS7 (shapes, social schema)",
		Family:   "snb",
		Dataset:  "social-1k",
		Fidelity: "derived",
		Queries:  shortQueries,
	})
}

var shortQueries = []*workload.Query{qIS1, qIS2, qIS3, qIS4, qIS5, qIS6, qIS7}

// qIS1 — IS1 shape: person profile by id.
var qIS1 = &workload.Query{
	ID:    "snb-is1",
	Class: engine.PointRead,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (p:Person {id: $personId})
RETURN p.firstName AS firstName, p.lastName AS lastName,
       p.birthday AS birthday, p.creationDate AS creationDate`,
	},
	PoolKey: PoolPersonID,
	Params:  workload.NewPoolSource(nil),
	Reference: &workload.RefStrategy{
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			m, err := loadSocial(ds)
			if err != nil {
				return nil, err
			}
			id, err := pstr(p, "personId")
			if err != nil {
				return nil, err
			}
			ans := &workload.Answer{Columns: []string{"firstName", "lastName", "birthday", "creationDate"}}
			if i, ok := m.personIx[id]; ok {
				pr := m.persons[i]
				ans.Rows = append(ans.Rows, row(pr.firstName, pr.lastName, pr.birthday, pr.creationDate))
			}
			return ans, nil
		},
	},
}

// qIS2 — IS2 shape: the person's 10 most recent posts, newest first.
var qIS2 = &workload.Query{
	ID:    "snb-is2",
	Class: engine.Traversal,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (p:Person {id: $personId})<-[:HAS_CREATOR]-(m:Post)
RETURN m.id AS postId, m.content AS content, m.creationDate AS creationDate
ORDER BY creationDate DESC, postId DESC
LIMIT 10`,
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
			var rows [][]engine.Value
			for _, pi := range m.postsOf[i] {
				po := m.posts[pi]
				rows = append(rows, row(idv(po.id), po.content, po.creationDate))
			}
			sortRows(rows, sortKey{col: 2, desc: true}, sortKey{col: 0, desc: true})
			return &workload.Answer{
				Columns: []string{"postId", "content", "creationDate"},
				Rows:    limit(rows, 10),
			}, nil
		},
	},
}

// qIS3 — IS3 shape: the person's friends with the friendship date, newest
// friendship first. The undirected match returns one row per stored KNOWS
// edge, so a pair connected in both directions yields two rows; the
// reference mirrors that exactly.
var qIS3 = &workload.Query{
	ID:    "snb-is3",
	Class: engine.Traversal,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (p:Person {id: $personId})-[k:KNOWS]-(f:Person)
RETURN f.id AS friendId, f.firstName AS firstName, f.lastName AS lastName,
       k.creationDate AS since
ORDER BY since DESC, friendId ASC`,
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
			var rows [][]engine.Value
			for _, e := range m.knows[i] {
				f := m.persons[e.other]
				rows = append(rows, row(idv(f.id), f.firstName, f.lastName, e.date))
			}
			sortRows(rows, sortKey{col: 3, desc: true}, sortKey{col: 0})
			return &workload.Answer{
				Columns: []string{"friendId", "firstName", "lastName", "since"},
				Rows:    rows,
			}, nil
		},
	},
}

// qIS4 — IS4 shape: one post's content and creation date.
var qIS4 = &workload.Query{
	ID:    "snb-is4",
	Class: engine.PointRead,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (m:Post {id: $postId})
RETURN m.content AS content, m.creationDate AS creationDate`,
	},
	PoolKey: PoolPostID,
	Params:  workload.NewPoolSource(nil),
	Reference: &workload.RefStrategy{
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			m, err := loadSocial(ds)
			if err != nil {
				return nil, err
			}
			id, err := pstr(p, "postId")
			if err != nil {
				return nil, err
			}
			ans := &workload.Answer{Columns: []string{"content", "creationDate"}}
			if i, ok := m.postIx[id]; ok {
				ans.Rows = append(ans.Rows, row(m.posts[i].content, m.posts[i].creationDate))
			}
			return ans, nil
		},
	},
}

// qIS5 — IS5 shape: one post's creator.
var qIS5 = &workload.Query{
	ID:    "snb-is5",
	Class: engine.PointRead,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (m:Post {id: $postId})-[:HAS_CREATOR]->(p:Person)
RETURN p.id AS personId, p.firstName AS firstName, p.lastName AS lastName`,
	},
	PoolKey: PoolPostID,
	Params:  workload.NewPoolSource(nil),
	Reference: &workload.RefStrategy{
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			m, err := loadSocial(ds)
			if err != nil {
				return nil, err
			}
			id, err := pstr(p, "postId")
			if err != nil {
				return nil, err
			}
			i, err := m.post(id)
			if err != nil {
				return nil, err
			}
			c := m.persons[m.postCreator[i]]
			return &workload.Answer{
				Columns: []string{"personId", "firstName", "lastName"},
				Rows:    [][]engine.Value{row(idv(c.id), c.firstName, c.lastName)},
			}, nil
		},
	},
}

// qIS6 — IS6 shape: the forum containing a post, with its title.
var qIS6 = &workload.Query{
	ID:    "snb-is6",
	Class: engine.Traversal,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (f:Forum)-[:CONTAINER_OF]->(m:Post {id: $postId})
RETURN f.id AS forumId, f.title AS title`,
	},
	PoolKey: PoolPostID,
	Params:  workload.NewPoolSource(nil),
	Reference: &workload.RefStrategy{
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			m, err := loadSocial(ds)
			if err != nil {
				return nil, err
			}
			id, err := pstr(p, "postId")
			if err != nil {
				return nil, err
			}
			i, err := m.post(id)
			if err != nil {
				return nil, err
			}
			ans := &workload.Answer{Columns: []string{"forumId", "title"}}
			if fi := m.postForum[i]; fi >= 0 {
				ans.Rows = append(ans.Rows, row(idv(m.forums[fi].id), m.forums[fi].title))
			}
			return ans, nil
		},
	},
}

// qIS7 — IS7 substitute (disclosed above): the likers of a post ordered by
// like date, newest first. Same fan-in shape as "replies to a message"; the
// schema has no Comment/REPLY_OF.
var qIS7 = &workload.Query{
	ID:    "snb-is7",
	Class: engine.Subgraph,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (p:Person)-[l:LIKES]->(m:Post {id: $postId})
RETURN p.id AS personId, p.firstName AS firstName, p.lastName AS lastName,
       l.creationDate AS likeDate
ORDER BY likeDate DESC, personId ASC`,
	},
	PoolKey: PoolPostID,
	Params:  workload.NewPoolSource(nil),
	Reference: &workload.RefStrategy{
		Compare: workload.CompareSpec{Ordered: true},
		Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
			m, err := loadSocial(ds)
			if err != nil {
				return nil, err
			}
			id, err := pstr(p, "postId")
			if err != nil {
				return nil, err
			}
			i, err := m.post(id)
			if err != nil {
				return nil, err
			}
			var rows [][]engine.Value
			for _, e := range m.postLikers[i] {
				liker := m.persons[e.other]
				rows = append(rows, row(idv(liker.id), liker.firstName, liker.lastName, e.date))
			}
			sortRows(rows, sortKey{col: 3, desc: true}, sortKey{col: 0})
			return &workload.Answer{
				Columns: []string{"personId", "firstName", "lastName", "likeDate"},
				Rows:    rows,
			}, nil
		},
	},
}
