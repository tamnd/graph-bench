package snb

import (
	"fmt"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

// This file registers "snb-update": the SNB Interactive v2 update shapes
// expressible on the social schema (spec 06 §3.3) — inserts IU1 (add
// person), IU3 (add like), IU6 (add post), IU8 (add friendship), and
// deletes ID2 (remove like), ID3 (remove post). The remaining IU/ID
// operations need Comments, Forums-with-moderators, or Tag edges the schema
// lacks. Engines without the Deletes capability run the IU subset only; the
// runner skips the ID queries via capabilities, they are still defined
// here.
//
// Every write is stationary by construction: Setup creates any
// prerequisite entities under fresh ids that cannot collide with generated
// data (the generator only emits non-negative ids, so the negative ids
// below are free), the
// PostCondition returns a single boolean row checking the effect, and
// Teardown removes everything Setup and the write created, restoring the
// pre-write state for the next repetition. Setup and Teardown run without
// parameter bindings, so they embed the same literal ids the Fixed params
// carry. All writes set AutocommitOK: they are single self-contained
// statements, safe outside an explicit transaction on engines without
// transaction support.

func init() {
	workload.Register(&workload.Workload{
		Name:     "snb-update",
		Title:    "SNB Interactive updates IU1/3/6/8 + ID2/3 (shapes, social schema)",
		Family:   "snb",
		Dataset:  "social-1k",
		Fidelity: "derived",
		Queries:  updateQueries,
	})
}

var updateQueries = []*workload.Query{qIU1, qIU3, qIU6, qIU8, qID2, qID3}

// insertQueries and deleteQueries split the update stream for the mix
// weights (06 §3.4: 20% insert, 0.2% delete).
var (
	insertQueries = []*workload.Query{qIU1, qIU3, qIU6, qIU8}
	deleteQueries = []*workload.Query{qID2, qID3}
)

// Fresh ids for write targets. The generators emit only non-negative ids, so
// a negative id is free at every scale.
//
// They are integers, not tokens like "P-1", because the canonical datasets
// carry integer ids: a strongly typed engine gives Person.id an integer
// column, and a string literal compared against it is a bind-time type error,
// not a lookup that misses. The error surfaces on teardown — the first
// statement bound against loaded data — where it reads as harness breakage
// instead of the type mismatch it is.
const (
	iu1PersonID = int64(-1)
	iu3PersonID = int64(-2)
	iu3PostID   = int64(-2)
	iu6AuthorID = int64(-6)
	iu6ForumID  = int64(-1)
	iu6PostID   = int64(-6)
	iu8PersonA  = int64(-8)
	iu8PersonB  = int64(-9)
	id2PersonID = int64(-10)
	id2PostID   = int64(-10)
	id3PostID   = int64(-11)
)

// seedPerson renders a throwaway Person creation pattern for Setup texts.
func seedPerson(id int64) string {
	return fmt.Sprintf(`(:Person {id: %d, firstName: "Seed", lastName: "Person", birthday: 0, creationDate: 0})`, id)
}

// seedPost renders a throwaway Post creation pattern for Setup texts.
func seedPost(id int64) string {
	return fmt.Sprintf(`(:Post {id: %d, content: "seed", creationDate: 0, creatorId: -1})`, id)
}

// qIU1 — IU1 shape: add a person.
var qIU1 = &workload.Query{
	ID:    "snb-iu1",
	Class: engine.Write,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `CREATE (p:Person {id: $personId, firstName: $firstName, lastName: $lastName,
                 birthday: $birthday, creationDate: $creationDate})`,
	},
	Params: workload.Fixed{P: workload.Params{
		"personId": iu1PersonID, "firstName": "Ada", "lastName": "Lovelace",
		"birthday": int64(158371200), "creationDate": int64(1234567),
	}},
	PostCondition: `MATCH (p:Person {id: $personId})
WITH count(p) AS c
RETURN c = 1 AS ok`,
	Teardown:     fmt.Sprintf(`MATCH (p:Person {id: %d}) DETACH DELETE p`, iu1PersonID),
	AutocommitOK: true,
}

// qIU3 — IU3 shape: add a like. Setup creates a dedicated person and post so
// the created edge is the only one between the pair.
var qIU3 = &workload.Query{
	ID:    "snb-iu3",
	Class: engine.Write,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (p:Person {id: $personId}), (m:Post {id: $postId})
CREATE (p)-[:LIKES {creationDate: $creationDate}]->(m)`,
	},
	Params: workload.Fixed{P: workload.Params{
		"personId": iu3PersonID, "postId": iu3PostID, "creationDate": int64(7654321),
	}},
	Setup: fmt.Sprintf(`CREATE %s, %s`, seedPerson(iu3PersonID), seedPost(iu3PostID)),
	PostCondition: `MATCH (p:Person {id: $personId})-[l:LIKES]->(m:Post {id: $postId})
WITH count(l) AS c
RETURN c = 1 AS ok`,
	Teardown: fmt.Sprintf(`MATCH (p:Person {id: %d}), (m:Post {id: %d}) DETACH DELETE p, m`,
		iu3PersonID, iu3PostID),
	AutocommitOK: true,
}

// qIU6 — IU6 shape (add post; IU6 numbering per the task, IU2 in the v2
// spec): create the post and wire its creator and containing forum.
var qIU6 = &workload.Query{
	ID:    "snb-iu6",
	Class: engine.Write,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (p:Person {id: $authorId}), (f:Forum {id: $forumId})
CREATE (m:Post {id: $postId, content: $content, creationDate: $creationDate, creatorId: -1})
CREATE (m)-[:HAS_CREATOR]->(p)
CREATE (f)-[:CONTAINER_OF]->(m)`,
	},
	Params: workload.Fixed{P: workload.Params{
		"authorId": iu6AuthorID, "forumId": iu6ForumID, "postId": iu6PostID,
		"content": "fresh post", "creationDate": int64(2345678),
	}},
	Setup: fmt.Sprintf(`CREATE %s, (:Forum {id: %d, title: "seed forum"})`,
		seedPerson(iu6AuthorID), iu6ForumID),
	PostCondition: `MATCH (f:Forum {id: $forumId})-[:CONTAINER_OF]->(m:Post {id: $postId})-[:HAS_CREATOR]->(p:Person {id: $authorId})
WITH count(m) AS c
RETURN c = 1 AS ok`,
	Teardown: fmt.Sprintf(`MATCH (p:Person {id: %d}), (f:Forum {id: %d}), (m:Post {id: %d}) DETACH DELETE p, f, m`,
		iu6AuthorID, iu6ForumID, iu6PostID),
	AutocommitOK: true,
}

// qIU8 — IU8 shape: add a friendship (dated KNOWS edge) between two
// dedicated persons.
var qIU8 = &workload.Query{
	ID:    "snb-iu8",
	Class: engine.Write,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (a:Person {id: $person1Id}), (b:Person {id: $person2Id})
CREATE (a)-[:KNOWS {creationDate: $creationDate}]->(b)`,
	},
	Params: workload.Fixed{P: workload.Params{
		"person1Id": iu8PersonA, "person2Id": iu8PersonB, "creationDate": int64(3456789),
	}},
	Setup: fmt.Sprintf(`CREATE %s, %s`, seedPerson(iu8PersonA), seedPerson(iu8PersonB)),
	PostCondition: `MATCH (a:Person {id: $person1Id})-[k:KNOWS]->(b:Person {id: $person2Id})
WITH count(k) AS c
RETURN c = 1 AS ok`,
	Teardown: fmt.Sprintf(`MATCH (a:Person {id: %d}), (b:Person {id: %d}) DETACH DELETE a, b`,
		iu8PersonA, iu8PersonB),
	AutocommitOK: true,
}

// qID2 — ID2 shape: remove a like. Setup creates the person, the post, and
// the LIKES edge the query deletes. Needs the Deletes capability.
var qID2 = &workload.Query{
	ID:    "snb-id2",
	Class: engine.Write,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (p:Person {id: $personId})-[l:LIKES]->(m:Post {id: $postId})
DELETE l`,
	},
	Params: workload.Fixed{P: workload.Params{
		"personId": id2PersonID, "postId": id2PostID,
	}},
	Setup: fmt.Sprintf(`CREATE (p:Person {id: %d, firstName: "Seed", lastName: "Person", birthday: 0, creationDate: 0}), %s
CREATE (p)-[:LIKES {creationDate: 1}]->(m)`, id2PersonID, seedPostBound(id2PostID)),
	PostCondition: `MATCH (p:Person {id: $personId})-[l:LIKES]->(m:Post {id: $postId})
WITH count(l) AS c
RETURN c = 0 AS ok`,
	Teardown: fmt.Sprintf(`MATCH (p:Person {id: %d}), (m:Post {id: %d}) DETACH DELETE p, m`,
		id2PersonID, id2PostID),
	AutocommitOK: true,
}

// seedPostBound is seedPost with a binding variable m, for setups that then
// attach an edge to the fresh post.
func seedPostBound(id int64) string {
	return fmt.Sprintf(`(m:Post {id: %d, content: "seed", creationDate: 0, creatorId: -1})`, id)
}

// qID3 — ID3 shape (post delete, ID6 in the v2 numbering): detach-delete a
// dedicated post. Teardown is a no-op when the delete succeeded and cleans
// the Setup residue when it did not. Needs the Deletes capability.
var qID3 = &workload.Query{
	ID:    "snb-id3",
	Class: engine.Write,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (m:Post {id: $postId})
DETACH DELETE m`,
	},
	Params: workload.Fixed{P: workload.Params{"postId": id3PostID}},
	Setup:  fmt.Sprintf(`CREATE %s`, seedPost(id3PostID)),
	PostCondition: `MATCH (m:Post {id: $postId})
WITH count(m) AS c
RETURN c = 0 AS ok`,
	Teardown:     fmt.Sprintf(`MATCH (m:Post {id: %d}) DETACH DELETE m`, id3PostID),
	AutocommitOK: true,
}
