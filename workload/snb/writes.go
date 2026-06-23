package snb

import (
	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
)

func init() {
	workload.Register(writeWorkload)
}

// writeWorkload is the "snb-write" workload: the SNB Interactive v2 write stream
// (eight inserts IU1-IU8 and eight deletes ID1-ID8) running in isolation.
// Isolation gives a clean per-write latency to compare against the write budget.
// The realistic mixed throughput is measured in the "snb-mix" workload.
//
// The write stream's parameters come from the generator's update stream, replayed
// in timestamp order. For now each write uses an empty pool (the update-stream
// loader fills the pool at run time when the dataset is loaded). The reference is
// a post-condition count check: after replaying a window, node and edge counts
// match the arithmetic of inserts minus deletes.
var writeWorkload = &workload.Workload{
	Name:    "snb-write",
	Title:   "SNB Interactive v2 write stream IU1-IU8 / ID1-ID8 (isolated)",
	Dataset: "snb-sf1",

	Queries: []*workload.WorkloadQuery{
		iu1(), iu2(), iu3(), iu4(), iu5(), iu6(), iu7(), iu8(),
		id1(), id2(), id3(), id4(), id5(), id6(), id7(), id8(),
	},
}

// writeRef is the RefStrategy for write operations. The reference answer is a
// post-condition count, not a row set; Compute is nil until the post-condition
// checker is wired. Comparison is unordered (writes return an empty result or a
// single status row). Validation records a skip (not a failure) while Compute is nil.
func writeRef() workload.RefStrategy {
	return workload.RefStrategy{
		Compare: workload.CompareSpec{
			Ordered:   false,
			CoerceNum: false,
		},
		Compute: nil,
	}
}

// -- inserts -----------------------------------------------------------------

// iu1: insert a person (IU1). Inserts the person node and its located-in,
// study-at, work-at, and has-interest edges. The multi-edge tails are unwound
// from the record's lists.
func iu1() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-iu1",
		Class: target.Write,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `CREATE (p:Person {
  id: $personId, firstName: $personFirstName, lastName: $personLastName,
  gender: $gender, birthday: $birthday, creationDate: $creationDate,
  locationIP: $locationIP, browserUsed: $browserUsed
})
WITH p
MATCH (city:Place {id: $cityId})
CREATE (p)-[:IS_LOCATED_IN]->(city)
WITH p
UNWIND $studyAt AS org
  MATCH (uni:Organisation {id: org.organisationId})
  CREATE (p)-[:STUDY_AT {classYear: org.classYear}]->(uni)
WITH p
UNWIND $workAt AS org
  MATCH (company:Organisation {id: org.organisationId})
  CREATE (p)-[:WORK_AT {workFrom: org.workFrom}]->(company)
WITH p
UNWIND $tagIds AS tid
  MATCH (t:Tag {id: tid})
  CREATE (p)-[:HAS_INTEREST]->(t)`,
		},
		Params:    workload.NewPool(nil),
		Reference: writeRef(),
	}
}

// iu2: insert a post (IU2). Creates the post node and its creator, container
// forum, location, and tag edges.
func iu2() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-iu2",
		Class: target.Write,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `CREATE (m:Message:Post {
  id: $messageId, imageFile: $imageFile, creationDate: $creationDate,
  locationIP: $locationIP, browserUsed: $browserUsed,
  language: $language, content: $content, length: $length
})
WITH m
MATCH (person:Person {id: $authorPersonId})
CREATE (m)-[:HAS_CREATOR]->(person)
WITH m
MATCH (forum:Forum {id: $forumId})
CREATE (forum)-[:CONTAINER_OF]->(m)
WITH m
MATCH (country:Place {id: $countryId})
CREATE (m)-[:IS_LOCATED_IN]->(country)
WITH m
UNWIND $tagIds AS tid
  MATCH (t:Tag {id: tid})
  CREATE (m)-[:HAS_TAG]->(t)`,
		},
		Params:    workload.NewPool(nil),
		Reference: writeRef(),
	}
}

// iu3: insert a like on a post (IU3).
func iu3() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-iu3",
		Class: target.Write,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (person:Person {id: $personId}),
      (post:Message:Post {id: $postId})
CREATE (person)-[:LIKES {creationDate: $creationDate}]->(post)`,
		},
		Params:    workload.NewPool(nil),
		Reference: writeRef(),
	}
}

// iu4: insert a forum (IU4). Creates the forum node and its moderator and tag
// edges.
func iu4() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-iu4",
		Class: target.Write,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `CREATE (f:Forum {
  id: $forumId, title: $title, creationDate: $creationDate
})
WITH f
MATCH (mod:Person {id: $moderatorPersonId})
CREATE (f)-[:HAS_MODERATOR]->(mod)
WITH f
UNWIND $tagIds AS tid
  MATCH (t:Tag {id: tid})
  CREATE (f)-[:HAS_TAG]->(t)`,
		},
		Params:    workload.NewPool(nil),
		Reference: writeRef(),
	}
}

// iu5: insert a forum membership (IU5).
func iu5() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-iu5",
		Class: target.Write,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (forum:Forum {id: $forumId}),
      (person:Person {id: $personId})
CREATE (forum)-[:HAS_MEMBER {joinDate: $joinDate}]->(person)`,
		},
		Params:    workload.NewPool(nil),
		Reference: writeRef(),
	}
}

// iu6: insert a friendship (IU6, the KNOWS edge with creation date).
func iu6() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-iu6",
		Class: target.Write,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (p1:Person {id: $person1Id}),
      (p2:Person {id: $person2Id})
CREATE (p1)-[:KNOWS {creationDate: $creationDate}]->(p2)`,
		},
		Params:    workload.NewPool(nil),
		Reference: writeRef(),
	}
}

// iu7: insert a comment (IU7). Creates the comment node and its creator,
// parent (reply-of), location, and tag edges.
func iu7() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-iu7",
		Class: target.Write,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `CREATE (m:Message:Comment {
  id: $messageId, creationDate: $creationDate,
  locationIP: $locationIP, browserUsed: $browserUsed,
  content: $content, length: $length
})
WITH m
MATCH (author:Person {id: $authorPersonId})
CREATE (m)-[:HAS_CREATOR]->(author)
WITH m
MATCH (parent:Message {id: $replyOfMessageId})
CREATE (m)-[:REPLY_OF]->(parent)
WITH m
MATCH (country:Place {id: $countryId})
CREATE (m)-[:IS_LOCATED_IN]->(country)
WITH m
UNWIND $tagIds AS tid
  MATCH (t:Tag {id: tid})
  CREATE (m)-[:HAS_TAG]->(t)`,
		},
		Params:    workload.NewPool(nil),
		Reference: writeRef(),
	}
}

// iu8: insert a like on a comment (IU8).
func iu8() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-iu8",
		Class: target.Write,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (person:Person {id: $personId}),
      (comment:Message:Comment {id: $commentId})
CREATE (person)-[:LIKES {creationDate: $creationDate}]->(comment)`,
		},
		Params:    workload.NewPool(nil),
		Reference: writeRef(),
	}
}

// -- deletes -----------------------------------------------------------------

// id1: delete a person with the v2 deep-delete cascade (ID1). First deletes the
// person's messages (which disconnects reply chains), then the person node.
// DETACH DELETE removes all incident edges.
func id1() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-id1",
		Class: target.Write,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (p:Person {id: $personId})
OPTIONAL MATCH (p)<-[:HAS_CREATOR]-(m:Message)
DETACH DELETE m
WITH p
DETACH DELETE p`,
		},
		Params:    workload.NewPool(nil),
		Reference: writeRef(),
	}
}

// id2: delete a friendship (the KNOWS edge between two persons) (ID2).
func id2() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-id2",
		Class: target.Write,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (p1:Person {id: $person1Id})-[r:KNOWS]-(p2:Person {id: $person2Id})
DELETE r`,
		},
		Params:    workload.NewPool(nil),
		Reference: writeRef(),
	}
}

// id3: delete a like on a post (ID3).
func id3() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-id3",
		Class: target.Write,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (person:Person {id: $personId})-[r:LIKES]->(post:Message:Post {id: $postId})
DELETE r`,
		},
		Params:    workload.NewPool(nil),
		Reference: writeRef(),
	}
}

// id4: delete a like on a comment (ID4).
func id4() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-id4",
		Class: target.Write,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (person:Person {id: $personId})-[r:LIKES]->(comment:Message:Comment {id: $commentId})
DELETE r`,
		},
		Params:    workload.NewPool(nil),
		Reference: writeRef(),
	}
}

// id5: delete a forum membership (ID5).
func id5() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-id5",
		Class: target.Write,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (forum:Forum {id: $forumId})-[r:HAS_MEMBER]->(person:Person {id: $personId})
DELETE r`,
		},
		Params:    workload.NewPool(nil),
		Reference: writeRef(),
	}
}

// id6: delete a post (ID6). DETACH DELETE removes its container-of and tag edges.
func id6() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-id6",
		Class: target.Write,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (post:Message:Post {id: $postId})
DETACH DELETE post`,
		},
		Params:    workload.NewPool(nil),
		Reference: writeRef(),
	}
}

// id7: delete a comment (ID7). DETACH DELETE removes its reply-of and tag edges.
func id7() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-id7",
		Class: target.Write,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (comment:Message:Comment {id: $commentId})
DETACH DELETE comment`,
		},
		Params:    workload.NewPool(nil),
		Reference: writeRef(),
	}
}

// id8: delete a forum (ID8). DETACH DELETE removes its HAS_MODERATOR,
// HAS_MEMBER, CONTAINER_OF, and HAS_TAG edges.
func id8() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-id8",
		Class: target.Write,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (forum:Forum {id: $forumId})
DETACH DELETE forum`,
		},
		Params:    workload.NewPool(nil),
		Reference: writeRef(),
	}
}
