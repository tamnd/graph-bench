package snb

import (
	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
)

func init() {
	workload.Register(complexHeavyWorkload)
}

// complexHeavyWorkload is the "snb-complex-heavy" workload: the eight SNB
// Interactive complex reads the curated subset (snb-complex) leaves out because
// they touch more of the graph or do path-finding, so they belong to the
// controlled-machine analytical tier rather than the CI runner. IC3, IC4, IC5,
// IC7, IC10, and IC12 are multi-hop expansions with heavier aggregation; IC13 and
// IC14 are person-to-person path queries (single shortest path and the weighted
// trusted-connection paths). All eight ship as the verbatim LDBC SNB Interactive
// v2 reference Cypher.
//
// They are class Analytical to mark the tier: the budget gate applies the heavy
// ceiling, and the CI smoke selection skips them. Their references are validated
// cross-engine (Compute is nil here, set up against real SNB data in the
// integration path), the same discipline the curated complex reads use.
//
// See notes/Spec/2060/bench/05-workloads.md section 3.2.
var complexHeavyWorkload = &workload.Workload{
	Name:    "snb-complex-heavy",
	Title:   "SNB Interactive heavy complex reads IC3/4/5/7/10/12/13/14 (controlled-machine tier)",
	Dataset: "snb-sf1",

	Queries: []*workload.WorkloadQuery{
		ic3(), ic4(), ic5(), ic7(), ic10(), ic12(), ic13(), ic14(),
	},
}

// heavyRef is the reference strategy shared by the heavy complex reads: unordered
// where the LDBC ORDER BY does not fully determine ties is avoided (each query
// sets Ordered per its own ORDER BY), numbers coerced because counts come back as
// int on some engines and float on others. Compute is nil; the engine-independent
// reference is wired against real SNB data in the integration path.
func heavyRef(ordered bool) workload.RefStrategy {
	return workload.RefStrategy{
		Compare: workload.CompareSpec{
			Ordered:   ordered,
			CoerceNum: true,
		},
		Compute: nil,
	}
}

// ic3: friends and friends-of-friends who posted messages in two given countries
// within a window. Two-hop KNOWS, location exclusion, message-country join, paired
// per-country counts.
func ic3() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-ic3",
		Class: target.Analytical,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (countryX:Country {name: $countryXName}),
      (countryY:Country {name: $countryYName}),
      (person:Person {id: $personId})
WITH person, countryX, countryY
LIMIT 1
MATCH (city:City)-[:IS_PART_OF]->(country:Country)
WHERE country IN [countryX, countryY]
WITH person, countryX, countryY, collect(city) AS cities
MATCH (person)-[:KNOWS*1..2]-(friend:Person)-[:IS_LOCATED_IN]->(city:City)
WHERE NOT person = friend AND NOT city IN cities
WITH DISTINCT friend, countryX, countryY
MATCH (friend)<-[:HAS_CREATOR]-(message:Message)-[:IS_LOCATED_IN]->(country:Country)
WHERE country IN [countryX, countryY]
  AND message.creationDate >= $startDate
  AND message.creationDate < $endDate
WITH friend,
     CASE WHEN country = countryX THEN 1 ELSE 0 END AS messageX,
     CASE WHEN country = countryY THEN 1 ELSE 0 END AS messageY
WITH friend, sum(messageX) AS xCount, sum(messageY) AS yCount
WHERE xCount > 0 AND yCount > 0
RETURN friend.id AS personId,
       friend.firstName AS personFirstName,
       friend.lastName AS personLastName,
       xCount, yCount, xCount + yCount AS count
ORDER BY count DESC, personId ASC
LIMIT 20`,
		},
		Params:    workload.NewPool(nil), // {personId, countryXName, countryYName, startDate, endDate}
		Reference: heavyRef(true),
	}
}

// ic4: new topics a person's friends posted about in a window but never before.
// One-hop KNOWS to friends' posts, tag window filter, exclude tags used earlier.
func ic4() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-ic4",
		Class: target.Analytical,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (person:Person {id: $personId})-[:KNOWS]-(friend:Person),
      (friend)<-[:HAS_CREATOR]-(post:Post)-[:HAS_TAG]->(tag:Tag)
WITH DISTINCT tag, post
WITH tag,
     CASE WHEN post.creationDate >= $startDate AND post.creationDate < $endDate THEN 1 ELSE 0 END AS valid,
     CASE WHEN post.creationDate < $startDate THEN 1 ELSE 0 END AS inValid
WITH tag, sum(valid) AS postCount, sum(inValid) AS inValidPostCount
WHERE postCount > 0 AND inValidPostCount = 0
RETURN tag.name AS tagName, postCount
ORDER BY postCount DESC, tagName ASC
LIMIT 10`,
		},
		Params:    workload.NewPool(nil), // {personId, startDate, endDate}
		Reference: heavyRef(true),
	}
}

// ic5: new groups a person's network joined after a date, counting each member's
// posts in the forum. Two-hop KNOWS, recent membership filter, post count.
func ic5() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-ic5",
		Class: target.Analytical,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (person:Person {id: $personId})-[:KNOWS*1..2]-(friend:Person)
WHERE NOT person = friend
WITH DISTINCT friend
MATCH (friend)<-[membership:HAS_MEMBER]-(forum:Forum)
WHERE membership.joinDate > $minDate
WITH forum, collect(friend) AS friends
OPTIONAL MATCH (friend)<-[:HAS_CREATOR]-(post:Post)<-[:CONTAINER_OF]-(forum)
WHERE friend IN friends
WITH forum, count(post) AS postCount
RETURN forum.title AS forumName, postCount
ORDER BY postCount DESC, forum.id ASC
LIMIT 20`,
		},
		Params:    workload.NewPool(nil), // {personId, minDate}
		Reference: heavyRef(true),
	}
}

// ic7: the most recent likers of a person's messages, with whether each like is
// new (the liker is not already a friend) and the latency from message to like.
func ic7() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-ic7",
		Class: target.Analytical,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (person:Person {id: $personId})<-[:HAS_CREATOR]-(message:Message)<-[like:LIKES]-(liker:Person)
WITH liker, message, like.creationDate AS likeTime, person
ORDER BY likeTime DESC, message.id ASC
WITH liker, head(collect(message)) AS message, head(collect(likeTime)) AS likeTime, person
RETURN liker.id AS personId,
       liker.firstName AS personFirstName,
       liker.lastName AS personLastName,
       likeTime AS likeCreationDate,
       message.id AS messageId,
       coalesce(message.content, message.imageFile) AS messageContent,
       floor(toFloat(likeTime - message.creationDate) / 1000.0 / 60.0) AS minutesLatency,
       NOT (liker)-[:KNOWS]-(person) AS isNew
ORDER BY likeCreationDate DESC, personId ASC
LIMIT 20`,
		},
		Params:    workload.NewPool(nil), // {personId}
		Reference: heavyRef(true),
	}
}

// ic10: friend recommendation by common interests. Exactly-two-hop KNOWS to
// non-friends with a birthday in the window, scored by posts that share a tag the
// start person is interested in versus posts that do not.
func ic10() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-ic10",
		Class: target.Analytical,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (person:Person {id: $personId})-[:KNOWS*2..2]-(friend:Person)-[:IS_LOCATED_IN]->(city:City)
WHERE NOT friend = person AND NOT (friend)-[:KNOWS]-(person)
WITH person, city, friend, friend.birthday AS birthday
WHERE (birthday.month = $month AND birthday.day >= 21)
   OR (birthday.month = ($month % 12) + 1 AND birthday.day < 22)
WITH DISTINCT friend, city, person
OPTIONAL MATCH (friend)<-[:HAS_CREATOR]-(post:Post)
WITH friend, city, collect(post) AS posts, person
WITH friend, city, person,
     size(posts) AS postCount,
     size([p IN posts WHERE (p)-[:HAS_TAG]->(:Tag)<-[:HAS_INTEREST]-(person)]) AS commonPostCount
RETURN friend.id AS personId,
       friend.firstName AS personFirstName,
       friend.lastName AS personLastName,
       commonPostCount - (postCount - commonPostCount) AS commonInterestScore,
       friend.gender AS personGender,
       city.name AS personCityName
ORDER BY commonInterestScore DESC, personId ASC
LIMIT 10`,
		},
		Params:    workload.NewPool(nil), // {personId, month}
		Reference: heavyRef(true),
	}
}

// ic12: expert search. Friends who replied to posts carrying a tag whose class is
// (or descends from) a given tag class, with the distinct tags and reply count.
func ic12() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-ic12",
		Class: target.Analytical,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (person:Person {id: $personId})-[:KNOWS]-(friend:Person)<-[:HAS_CREATOR]-(comment:Comment)-[:REPLY_OF]->(:Post)-[:HAS_TAG]->(tag:Tag)-[:HAS_TYPE]->(:TagClass)-[:IS_SUBCLASS_OF*0..]->(baseTagClass:TagClass)
WHERE baseTagClass.name = $tagClassName
RETURN friend.id AS personId,
       friend.firstName AS personFirstName,
       friend.lastName AS personLastName,
       collect(DISTINCT tag.name) AS tagNames,
       count(DISTINCT comment) AS replyCount
ORDER BY replyCount DESC, personId ASC
LIMIT 20`,
		},
		Params:    workload.NewPool(nil), // {personId, tagClassName}
		Reference: heavyRef(true),
	}
}

// ic13: single shortest path between two persons over KNOWS, returning the path
// length or -1 when there is no path. The canonical unweighted path query.
func ic13() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-ic13",
		Class: target.Analytical,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (person1:Person {id: $person1Id}), (person2:Person {id: $person2Id})
OPTIONAL MATCH path = shortestPath((person1)-[:KNOWS*]-(person2))
RETURN
  CASE path IS NULL
    WHEN true THEN -1
    ELSE length(path)
  END AS shortestPathLength`,
			// Kuzu has no shortestPath(); the SHORTEST keyword with an unbounded upper
			// limit finds the same path length. The CASE for the no-path case differs
			// in shape but returns the same -1 sentinel.
			workload.KuzuCypher: `MATCH (person1:Person {id: $person1Id}), (person2:Person {id: $person2Id})
OPTIONAL MATCH (person1)-[r:KNOWS* SHORTEST 1..]-(person2)
RETURN coalesce(length(r), -1) AS shortestPathLength`,
		},
		Params:    workload.NewPool(nil), // {person1Id, person2Id}
		Reference: heavyRef(true),
	}
}

// ic14: trusted connection paths. All shortest KNOWS paths between two persons,
// each weighted by the reply interactions between consecutive persons on the path
// (a reply to a post counts 1.0, a reply to a comment 0.5), ranked by weight.
func ic14() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-ic14",
		Class: target.Analytical,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH path = allShortestPaths((person1:Person {id: $person1Id})-[:KNOWS*0..]-(person2:Person {id: $person2Id}))
WITH nodes(path) AS pathNodes
RETURN
  [n IN pathNodes | n.id] AS personIdsInPath,
  reduce(weight = 0.0, idx IN range(0, size(pathNodes) - 2) |
    weight
    + size([(pathNodes[idx])<-[:HAS_CREATOR]-(c:Comment)-[:REPLY_OF]->(:Post)<-[:HAS_CREATOR]-(pathNodes[idx + 1]) | c]) * 1.0
    + size([(pathNodes[idx + 1])<-[:HAS_CREATOR]-(c:Comment)-[:REPLY_OF]->(:Post)<-[:HAS_CREATOR]-(pathNodes[idx]) | c]) * 1.0
    + size([(pathNodes[idx])<-[:HAS_CREATOR]-(c1:Comment)-[:REPLY_OF]->(:Comment)<-[:HAS_CREATOR]-(pathNodes[idx + 1]) | c1]) * 0.5
    + size([(pathNodes[idx + 1])<-[:HAS_CREATOR]-(c1:Comment)-[:REPLY_OF]->(:Comment)<-[:HAS_CREATOR]-(pathNodes[idx]) | c1]) * 0.5
  ) AS pathWeight
ORDER BY pathWeight DESC`,
		},
		Params:    workload.NewPool(nil), // {person1Id, person2Id}
		Reference: heavyRef(false),       // ties on pathWeight are not fully ordered
	}
}
