// Package snb holds the LDBC SNB Interactive short reads (IS1-IS7) and curated
// complex reads, plus the write stream and the realistic mixed workload. This
// file registers the "snb-short" workload: the seven short reads that run in
// isolation for clean per-query latency.
//
// IS1, IS4, and IS5 are class PointRead (single-entity lookups). IS2, IS3, IS6,
// and IS7 are class Traversal (one- or two-hop expansions). The Cypher texts are
// the verbatim LDBC SNB Interactive v2 reference queries. Parameters are drawn
// from the dataset's curated person and message pools; the pool pointer is
// replaced at run time when the dataset is loaded. An empty pool here means the
// query is skipped during validation (the gate records a skip, not a failure).
//
// See notes/Spec/2060/bench/05-workloads.md section 3.1 for the full design
// rationale and the per-query class assignment.
package snb

import (
	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
)

func init() {
	workload.Register(shortWorkload)
}

// shortWorkload is the "snb-short" workload: the seven SNB Interactive short
// reads running in isolation. No Mix: isolation gives a clean per-query p50/p99
// so a budget check can be attributed to one query.
var shortWorkload = &workload.Workload{
	Name:    "snb-short",
	Title:   "SNB Interactive IS1-IS7 short reads (isolated)",
	Dataset: "snb-sf1",

	Queries: []*workload.WorkloadQuery{
		is1(), is2(), is3(), is4(), is5(), is6(), is7(),
	},
}

// is1: a person's profile -- name, birthday, location, contact info.
// Class PointRead: single entity resolved by id, properties returned.
func is1() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-is1",
		Class: target.PointRead,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (n:Person {id: $personId})-[:IS_LOCATED_IN]->(p:Place)
RETURN n.firstName, n.lastName, n.birthday,
       n.locationIP, n.browserUsed, p.id AS cityId,
       n.gender, n.creationDate`,
		},
		Params:    workload.NewPool(nil), // replaced at load time with the curated person pool
		Reference: personRef(),
	}
}

// is2: a person's ten most recent messages, with the original post each replies to.
// Class Traversal: one-hop to messages, then variable-length to the root post.
func is2() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-is2",
		Class: target.Traversal,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (:Person {id: $personId})<-[:HAS_CREATOR]-(m:Message)
WITH m ORDER BY m.creationDate DESC, m.id DESC LIMIT 10
MATCH (m)-[:REPLY_OF*0..]->(p:Post)-[:HAS_CREATOR]->(c:Person)
RETURN m.id, coalesce(m.content, m.imageFile), m.creationDate,
       p.id, c.id, c.firstName, c.lastName
ORDER BY m.creationDate DESC, m.id DESC`,
		},
		Params: workload.NewPool(nil),
		Reference: workload.RefStrategy{
			Compare: workload.CompareSpec{
				Ordered:   true, // ORDER BY creationDate DESC, id DESC is stable
				CoerceNum: false,
			},
			Compute: nil,
		},
	}
}

// is3: a person's friends and the date each friendship was formed.
// Class Traversal: one-hop KNOWS traversal.
func is3() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-is3",
		Class: target.Traversal,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (n:Person {id: $personId})-[r:KNOWS]-(friend:Person)
RETURN friend.id, friend.firstName, friend.lastName,
       r.creationDate AS friendshipCreationDate
ORDER BY friendshipCreationDate DESC, friend.id ASC`,
		},
		Params: workload.NewPool(nil),
		Reference: workload.RefStrategy{
			Compare: workload.CompareSpec{
				Ordered:   true, // ORDER BY friendshipCreationDate DESC, friend.id ASC is stable
				CoerceNum: false,
			},
			Compute: nil,
		},
	}
}

// is4: the content and creation date of one message.
// Class PointRead: single entity resolved by id.
func is4() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-is4",
		Class: target.PointRead,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (m:Message {id: $messageId})
RETURN m.creationDate, coalesce(m.content, m.imageFile) AS content`,
		},
		Params:    workload.NewPool(nil), // replaced with curated message pool at load time
		Reference: messageRef(),
	}
}

// is5: the creator of one message.
// Class PointRead: one-hop but the result is a single person entity; the hop
// count is one but the work is bounded: find the creator, return three fields.
func is5() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-is5",
		Class: target.PointRead,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (m:Message {id: $messageId})-[:HAS_CREATOR]->(p:Person)
RETURN p.id, p.firstName, p.lastName`,
		},
		Params:    workload.NewPool(nil),
		Reference: messageRef(),
	}
}

// is6: the forum a message belongs to and that forum's moderator.
// Class Traversal: variable-length REPLY_OF* to the root post, then two hops.
func is6() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-is6",
		Class: target.Traversal,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (m:Message {id: $messageId})-[:REPLY_OF*0..]->(p:Post)
      <-[:CONTAINER_OF]-(f:Forum)-[:HAS_MODERATOR]->(mod:Person)
RETURN f.id, f.title, mod.id, mod.firstName, mod.lastName`,
		},
		Params:    workload.NewPool(nil),
		Reference: messageRef(),
	}
}

// is7: the direct replies to one message and whether each replier knows the author.
// Class Traversal: one-hop REPLY_OF, OPTIONAL MATCH for the knows check.
func is7() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-is7",
		Class: target.Traversal,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (m:Message {id: $messageId})<-[:REPLY_OF]-(c:Comment)-[:HAS_CREATOR]->(p:Person)
OPTIONAL MATCH (m)-[:HAS_CREATOR]->(a:Person)-[k:KNOWS]-(p)
RETURN c.id, c.content, c.creationDate,
       p.id, p.firstName, p.lastName,
       k IS NOT NULL AS knowsAuthor
ORDER BY c.creationDate DESC, p.id ASC`,
		},
		Params: workload.NewPool(nil),
		Reference: workload.RefStrategy{
			Compare: workload.CompareSpec{
				Ordered:   true, // ORDER BY creationDate DESC, p.id ASC is stable
				CoerceNum: false,
			},
			Compute: nil,
		},
	}
}

// personRef is the RefStrategy for queries parameterized by a person id.
// Result structure varies per query, so Compute is nil until a per-query oracle
// is wired in M7d. The compare rule is unordered by default; queries with a
// stable ORDER BY override it inline.
func personRef() workload.RefStrategy {
	return workload.RefStrategy{
		Compare: workload.CompareSpec{
			Ordered:   false,
			CoerceNum: false,
		},
		Compute: nil,
	}
}

// messageRef is the RefStrategy for queries parameterized by a message id.
func messageRef() workload.RefStrategy {
	return workload.RefStrategy{
		Compare: workload.CompareSpec{
			Ordered:   false,
			CoerceNum: false,
		},
		Compute: nil,
	}
}
