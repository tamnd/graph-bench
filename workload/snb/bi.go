package snb

import (
	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
)

func init() {
	workload.Register(biWorkload)
}

// biWorkload is the "snb-bi" workload: the twenty LDBC SNB Business Intelligence
// read queries (BI1-BI20). They are the heavy analytical tier: each touches a
// large fraction of the graph, several do multi-way aggregation, and BI15, BI19,
// and BI20 are weighted-path queries. All twenty ship as openCypher close to the
// LDBC SNB BI v2 reference, class Analytical, run in isolation (no Mix).
//
// They are deferred to the controlled machine: at SF100 the working set is tens
// of gigabytes and a single query runs for seconds to minutes, so they do not fit
// the CI runner. References are validated cross-engine (Compute nil here, wired
// against real SNB data in the integration path). BI11 (friendship triangles in a
// country within a window) and BI17 (information propagation across forums sharing
// a tag) are the cyclic and mixed-join archetypes the suite watches for the WCOJ
// story.
//
// The three weighted-path queries (BI15, BI19, BI20) need a weighted-shortest-path
// procedure that openCypher cannot express natively; the texts here carry the
// path shape and the per-edge weight, and the per-engine procedure text is the
// capability seam (engines without it show a blank cell).
//
// See notes/Spec/2060/bench/05-workloads.md section 8.1.
var biWorkload = &workload.Workload{
	Name:    "snb-bi",
	Title:   "SNB Business Intelligence reads BI1-BI20 (controlled-machine analytical tier)",
	Dataset: "snb-sf1",

	Queries: []*workload.WorkloadQuery{
		bi1(), bi2(), bi3(), bi4(), bi5(), bi6(), bi7(), bi8(), bi9(), bi10(),
		bi11(), bi12(), bi13(), bi14(), bi15(), bi16(), bi17(), bi18(), bi19(), bi20(),
	},
}

// biRef is the reference strategy shared by the BI queries: Ordered set per query
// from whether its ORDER BY fully determines the order, numbers coerced because
// the heavy aggregates come back as int or float depending on the engine. Compute
// is nil; the engine-independent reference is wired against real SNB data.
func biRef(ordered bool) workload.RefStrategy {
	return workload.RefStrategy{
		Compare: workload.CompareSpec{Ordered: ordered, CoerceNum: true},
		Compute: nil,
	}
}

func biQuery(id, cypher string, ordered bool) *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:        id,
		Class:     target.Analytical,
		Texts:     map[workload.Dialect]string{workload.Cypher: cypher},
		Params:    workload.NewPool(nil),
		Reference: biRef(ordered),
	}
}

// bi1: posting summary. Messages before a date bucketed by year, comment flag, and
// length category, with counts, average length, and share of the total.
func bi1() *workload.WorkloadQuery {
	return biQuery("snb-bi1", `MATCH (message:Message)
WHERE message.creationDate < $datetime
WITH toFloat(count(message)) AS totalMessageCount
MATCH (message:Message)
WHERE message.creationDate < $datetime AND message.content IS NOT NULL
WITH totalMessageCount, message, message.creationDate.year AS year
WITH totalMessageCount, year,
     message:Comment AS isComment,
     CASE
       WHEN message.length < 40  THEN 0
       WHEN message.length < 80  THEN 1
       WHEN message.length < 160 THEN 2
       ELSE 3
     END AS lengthCategory,
     count(message) AS messageCount,
     sum(message.length) / toFloat(count(message)) AS averageMessageLength,
     sum(message.length) AS sumMessageLength
RETURN year, isComment, lengthCategory, messageCount,
       averageMessageLength, sumMessageLength,
       messageCount / totalMessageCount AS percentageOfMessages
ORDER BY year DESC, isComment ASC, lengthCategory ASC`, true)
}

// bi2: tag evolution. Message counts per tag of a class across two adjacent
// 100-day windows, ranked by the absolute difference.
func bi2() *workload.WorkloadQuery {
	return biQuery("snb-bi2", `WITH $date AS date1, $date + duration({days: 100}) AS date2
MATCH (tag:Tag)-[:HAS_TYPE]->(:TagClass {name: $tagClass})
OPTIONAL MATCH (message1:Message)-[:HAS_TAG]->(tag)
  WHERE date1 <= message1.creationDate AND message1.creationDate < date1 + duration({days: 100})
WITH tag, date2, count(message1) AS countWindow1
OPTIONAL MATCH (message2:Message)-[:HAS_TAG]->(tag)
  WHERE date2 <= message2.creationDate AND message2.creationDate < date2 + duration({days: 100})
WITH tag, countWindow1, count(message2) AS countWindow2
RETURN tag.name AS tagName, countWindow1, countWindow2,
       abs(countWindow1 - countWindow2) AS diff
ORDER BY diff DESC, tagName ASC
LIMIT 100`, true)
}

// bi3: popular topics in a country. Forums moderated by persons in a country, the
// messages under their posts carrying a tag of a class, counted per forum.
func bi3() *workload.WorkloadQuery {
	return biQuery("snb-bi3", `MATCH
  (country:Country {name: $country})<-[:IS_PART_OF]-(:City)<-[:IS_LOCATED_IN]-
  (person:Person)<-[:HAS_MODERATOR]-(forum:Forum)-[:CONTAINER_OF]->
  (post:Post)<-[:REPLY_OF*0..]-(message:Message)-[:HAS_TAG]->
  (:Tag)-[:HAS_TYPE]->(:TagClass {name: $tagClass})
RETURN forum.id AS forumId, forum.title AS forumTitle, forum.creationDate AS forumCreationDate,
       person.id AS personId, count(DISTINCT message) AS messageCount
ORDER BY messageCount DESC, forumId ASC
LIMIT 20`, true)
}

// bi4: top message creators by country. The 100 most popular forums per country
// by recent membership, then the top message creators within them.
func bi4() *workload.WorkloadQuery {
	return biQuery("snb-bi4", `MATCH (country:Country)<-[:IS_PART_OF]-(:City)<-[:IS_LOCATED_IN]-(person:Person)<-[:HAS_MEMBER]-(forum:Forum)
WHERE forum.creationDate > $date
WITH country, forum, count(person) AS numberOfMembers
ORDER BY numberOfMembers DESC, forum.id ASC, country.name ASC
WITH country, collect(forum)[0..100] AS popularForums
UNWIND popularForums AS forum
MATCH (forum)-[:CONTAINER_OF]->(post:Post)<-[:REPLY_OF*0..]-(message:Message)-[:HAS_CREATOR]->(person:Person)-[:IS_LOCATED_IN]->(:City)-[:IS_PART_OF]->(country)
RETURN forum.id AS forumId, forum.title AS forumTitle, forum.creationDate AS forumCreationDate,
       person.id AS personId, count(DISTINCT message) AS messageCount
ORDER BY messageCount DESC, personId ASC, forumId ASC
LIMIT 100`, true)
}

// bi5: most active posters of a given topic. Per person, the messages tagged with
// the topic plus the likes and replies they drew, scored 1/2/10.
func bi5() *workload.WorkloadQuery {
	return biQuery("snb-bi5", `MATCH (tag:Tag {name: $tag})<-[:HAS_TAG]-(message:Message)-[:HAS_CREATOR]->(person:Person)
OPTIONAL MATCH (message)<-[likes:LIKES]-(:Person)
OPTIONAL MATCH (message)<-[:REPLY_OF]-(reply:Comment)
WITH person,
     count(DISTINCT likes) AS likeCount,
     count(DISTINCT reply) AS replyCount,
     count(DISTINCT message) AS messageCount
RETURN person.id AS personId, replyCount, likeCount, messageCount,
       1 * messageCount + 2 * replyCount + 10 * likeCount AS score
ORDER BY score DESC, personId ASC
LIMIT 100`, true)
}

// bi6: most authoritative users on a given topic. Persons whose tagged messages
// were liked by persons whose own messages are themselves widely liked.
func bi6() *workload.WorkloadQuery {
	return biQuery("snb-bi6", `MATCH (tag:Tag {name: $tag})<-[:HAS_TAG]-(message1:Message)-[:HAS_CREATOR]->(person1:Person)
OPTIONAL MATCH (message1)<-[:LIKES]-(person2:Person)
OPTIONAL MATCH (person2)<-[:HAS_CREATOR]-(message2:Message)<-[likes:LIKES]-(person3:Person)
RETURN person1.id AS person1Id, count(DISTINCT likes) AS authorityScore
ORDER BY authorityScore DESC, person1Id ASC
LIMIT 100`, true)
}

// bi7: related topics. Tags that appear on comments replying to messages carrying
// a given tag, where the comment itself does not carry that tag.
func bi7() *workload.WorkloadQuery {
	return biQuery("snb-bi7", `MATCH (tag:Tag {name: $tag})<-[:HAS_TAG]-(message:Message),
      (message)<-[:REPLY_OF]-(comment:Comment)-[:HAS_TAG]->(relatedTag:Tag)
WHERE NOT (comment)-[:HAS_TAG]->(tag)
RETURN relatedTag.name AS relatedTagName, count(DISTINCT comment) AS count
ORDER BY count DESC, relatedTagName ASC
LIMIT 100`, true)
}

// bi8: central person for a tag. Each person's own score (100 per interest plus
// one per tagged message in a window) plus the summed scores of their friends.
func bi8() *workload.WorkloadQuery {
	return biQuery("snb-bi8", `MATCH (tag:Tag {name: $tag})
MATCH (person:Person)
OPTIONAL MATCH (person)-[interest:HAS_INTEREST]->(tag)
OPTIONAL MATCH (person)<-[:HAS_CREATOR]-(message:Message)-[:HAS_TAG]->(tag)
  WHERE $startDate <= message.creationDate AND message.creationDate <= $endDate
WITH person, 100 * count(DISTINCT interest) + count(DISTINCT message) AS score
WHERE score > 0
OPTIONAL MATCH (person)-[:KNOWS]-(friend:Person)
OPTIONAL MATCH (friend)-[friendInterest:HAS_INTEREST]->(tag)
OPTIONAL MATCH (friend)<-[:HAS_CREATOR]-(friendMessage:Message)-[:HAS_TAG]->(tag)
  WHERE $startDate <= friendMessage.creationDate AND friendMessage.creationDate <= $endDate
WITH person, score, friend,
     100 * count(DISTINCT friendInterest) + count(DISTINCT friendMessage) AS friendScore
WITH person, score, sum(friendScore) AS friendsScore
RETURN person.id AS personId, score, friendsScore
ORDER BY score + friendsScore DESC, personId ASC
LIMIT 100`, true)
}

// bi9: top thread initiators. Persons ranked by the total messages in the reply
// trees rooted at the posts they created within a window.
func bi9() *workload.WorkloadQuery {
	return biQuery("snb-bi9", `MATCH (person:Person)<-[:HAS_CREATOR]-(post:Post)<-[:REPLY_OF*0..]-(message:Message)
WHERE post.creationDate >= $startDate AND post.creationDate <= $endDate
  AND message.creationDate >= $startDate AND message.creationDate <= $endDate
WITH person, post, count(message) AS messageCount
RETURN person.id AS personId, person.firstName AS personFirstName, person.lastName AS personLastName,
       count(post) AS threadCount, sum(messageCount) AS messageCount
ORDER BY messageCount DESC, personId ASC
LIMIT 100`, true)
}

// bi10: experts in social circle. Persons at a chosen KNOWS distance from a start
// person, located in a country, with messages tagged under a tag class.
func bi10() *workload.WorkloadQuery {
	// Neo4j cannot parameterize the variable-length bound, so the path distance is
	// a literal here; the LDBC driver string-substitutes $minPathDistance and
	// $maxPathDistance (the common parameterization is 3..4).
	return biQuery("snb-bi10", `MATCH (person:Person {id: $personId})-[:KNOWS*3..4]-(expert:Person)-[:IS_LOCATED_IN]->(:City)-[:IS_PART_OF]->(:Country {name: $country}),
      (expert)<-[:HAS_CREATOR]-(message:Message)-[:HAS_TAG]->(tag:Tag)-[:HAS_TYPE]->(:TagClass {name: $tagClass})
WITH DISTINCT expert, message, tag
RETURN expert.id AS expertId, tag.name AS tagName, count(DISTINCT message) AS messageCount
ORDER BY messageCount DESC, tagName ASC, expertId ASC
LIMIT 100`, true)
}

// bi11: friend triangles. Triangles of persons in a country whose three KNOWS
// edges all fall inside a window. The cyclic 3-clique archetype.
func bi11() *workload.WorkloadQuery {
	return biQuery("snb-bi11", `MATCH
  (country:Country {name: $country})<-[:IS_PART_OF]-(:City)<-[:IS_LOCATED_IN]-(a:Person),
  (country)<-[:IS_PART_OF]-(:City)<-[:IS_LOCATED_IN]-(b:Person),
  (country)<-[:IS_PART_OF]-(:City)<-[:IS_LOCATED_IN]-(c:Person),
  (a)-[k1:KNOWS]-(b), (b)-[k2:KNOWS]-(c), (c)-[k3:KNOWS]-(a)
WHERE a.id < b.id AND b.id < c.id
  AND $startDate <= k1.creationDate AND k1.creationDate <= $endDate
  AND $startDate <= k2.creationDate AND k2.creationDate <= $endDate
  AND $startDate <= k3.creationDate AND k3.creationDate <= $endDate
RETURN count(*) AS count`, false)
}

// bi12: how many persons have a given number of messages. Distribution of per
// person message counts over short messages in a language set after a date.
func bi12() *workload.WorkloadQuery {
	return biQuery("snb-bi12", `MATCH (person:Person)
OPTIONAL MATCH (person)<-[:HAS_CREATOR]-(message:Message)-[:REPLY_OF*0..]->(post:Post)
WHERE message.content IS NOT NULL
  AND message.length < $lengthThreshold
  AND message.creationDate > $datetime
  AND post.language IN $languages
WITH person, count(message) AS messageCount
WITH messageCount, count(person) AS personCount
RETURN messageCount, personCount
ORDER BY personCount DESC, messageCount DESC`, true)
}

// bi13: zombies in a country. Low-activity accounts (zombies) in a country and the
// fraction of their likes that come from other zombies.
func bi13() *workload.WorkloadQuery {
	return biQuery("snb-bi13", `MATCH (country:Country {name: $country})<-[:IS_PART_OF]-(:City)<-[:IS_LOCATED_IN]-(zombie:Person)
WHERE zombie.creationDate < $endDate
OPTIONAL MATCH (zombie)<-[:HAS_CREATOR]-(message:Message)
WHERE message.creationDate < $endDate
WITH zombie,
     $endDate.year * 12 + $endDate.month
       - (zombie.creationDate.year * 12 + zombie.creationDate.month) + 1 AS months,
     count(message) AS messageCount
WITH zombie, 12.0 * (toFloat(messageCount) / months) AS zombieScore
WHERE zombieScore < 1
OPTIONAL MATCH (zombie)<-[:HAS_CREATOR]-(:Message)<-[:LIKES]-(likerZombie:Person)
WHERE likerZombie.creationDate < $endDate
WITH zombie, count(DISTINCT likerZombie) AS zombieLikeCount
OPTIONAL MATCH (zombie)<-[:HAS_CREATOR]-(:Message)<-[:LIKES]-(likerPerson:Person)
WHERE likerPerson.creationDate < $endDate
WITH zombie, zombieLikeCount, count(DISTINCT likerPerson) AS totalLikeCount
RETURN zombie.id AS zombieId, zombieLikeCount, totalLikeCount,
       CASE totalLikeCount WHEN 0 THEN 0.0 ELSE toFloat(zombieLikeCount) / totalLikeCount END AS zombieScore
ORDER BY zombieScore DESC, zombieId ASC
LIMIT 100`, true)
}

// bi14: international dialog. The top-scoring pair of friends across two countries
// per city, scored by their reply and like interactions.
func bi14() *workload.WorkloadQuery {
	return biQuery("snb-bi14", `MATCH (country1:Country {name: $country1})<-[:IS_PART_OF]-(city1:City)<-[:IS_LOCATED_IN]-(person1:Person)-[:KNOWS]-(person2:Person)-[:IS_LOCATED_IN]->(:City)-[:IS_PART_OF]->(:Country {name: $country2})
WITH person1, person2, city1,
     size([(person1)<-[:HAS_CREATOR]-(c:Comment)-[:REPLY_OF]->(:Message)-[:HAS_CREATOR]->(person2) | c]) AS c,
     size([(person1)<-[:HAS_CREATOR]-(:Message)<-[:REPLY_OF]-(cc:Comment)-[:HAS_CREATOR]->(person2) | cc]) AS r,
     size([(person1)-[:LIKES]->(:Message)-[:HAS_CREATOR]->(person2) | 1]) AS l1,
     size([(person1)<-[:HAS_CREATOR]-(:Message)<-[:LIKES]-(person2) | 1]) AS l2
WITH person1, person2, city1,
     CASE WHEN c > 0 THEN 4 ELSE 0 END +
     CASE WHEN r > 0 THEN 1 ELSE 0 END +
     CASE WHEN l1 > 0 THEN 10 ELSE 0 END +
     CASE WHEN l2 > 0 THEN 1 ELSE 0 END AS score
WHERE score > 0
ORDER BY city1.name ASC, score DESC, person1.id ASC, person2.id ASC
WITH city1, collect({p1: person1.id, p2: person2.id, score: score})[0] AS top
RETURN top.p1 AS person1Id, top.p2 AS person2Id, city1.name AS city1Name, top.score AS score
ORDER BY score DESC, person1Id ASC, person2Id ASC
LIMIT 100`, true)
}

// bi15: weighted interaction paths. The cheapest weighted KNOWS path between two
// persons, where edge weight derives from reply interactions in a window. The
// weighted shortest path needs a procedure openCypher cannot express; the text
// carries the path shape and the per-edge weight.
func bi15() *workload.WorkloadQuery {
	return biQuery("snb-bi15", `MATCH (person1:Person {id: $person1Id}), (person2:Person {id: $person2Id})
OPTIONAL MATCH path = shortestPath((person1)-[:KNOWS*]-(person2))
WITH person1, person2, path
RETURN
  CASE WHEN path IS NULL THEN [] ELSE [n IN nodes(path) | n.id] END AS personIdsInPath,
  CASE WHEN path IS NULL THEN -1.0
       ELSE reduce(weight = 0.0, k IN relationships(path) |
         weight + reduce(w = 0.0, m IN [(pa)<-[:HAS_CREATOR]-(m1:Message)-[:REPLY_OF]-(m2:Message)-[:HAS_CREATOR]->(pb)
           WHERE pa = startNode(k) AND pb = endNode(k)
             AND $startDate <= m1.creationDate AND m1.creationDate <= $endDate
             AND $startDate <= m2.creationDate AND m2.creationDate <= $endDate | m1] | w + 1.0))
  END AS pathWeight
ORDER BY pathWeight DESC`, false)
}

// bi16: fake news detection. Persons who posted messages tagged with two given
// tags on two given days, counted per tag.
func bi16() *workload.WorkloadQuery {
	return biQuery("snb-bi16", `MATCH (person:Person)<-[:HAS_CREATOR]-(messageA:Message)-[:HAS_TAG]->(:Tag {name: $tagA})
WHERE messageA.creationDate >= $dateA AND messageA.creationDate < $dateA + duration({days: 1})
WITH person, count(DISTINCT messageA) AS messageCountA
WHERE messageCountA > 0
MATCH (person)<-[:HAS_CREATOR]-(messageB:Message)-[:HAS_TAG]->(:Tag {name: $tagB})
WHERE messageB.creationDate >= $dateB AND messageB.creationDate < $dateB + duration({days: 1})
WITH person, messageCountA, count(DISTINCT messageB) AS messageCountB
WHERE messageCountB > 0
RETURN person.id AS personId, messageCountA, messageCountB
ORDER BY messageCountA DESC, messageCountB DESC, personId ASC
LIMIT 20`, true)
}

// bi17: information propagation analysis. A message on a tag in one forum, a reply
// chain across members of that forum who also reply in a second forum on the same
// tag within delta hours. Cycle plus tree-shaped joins.
func bi17() *workload.WorkloadQuery {
	return biQuery("snb-bi17", `MATCH
  (tag:Tag {name: $tag}),
  (person1:Person)<-[:HAS_CREATOR]-(message1:Message)-[:REPLY_OF*0..]->(post1:Post)<-[:CONTAINER_OF]-(forum1:Forum),
  (message1)-[:HAS_TAG]->(tag),
  (forum1)-[:HAS_MEMBER]->(person2:Person),
  (forum1)-[:HAS_MEMBER]->(person3:Person),
  (person2)<-[:HAS_CREATOR]-(comment:Comment)-[:HAS_TAG]->(tag),
  (comment)-[:REPLY_OF]->(message2:Message)-[:HAS_CREATOR]->(person3),
  (message2)-[:HAS_TAG]->(tag),
  (forum2:Forum)-[:CONTAINER_OF]->(message2)
WHERE forum1 <> forum2
  AND message2.creationDate > message1.creationDate + duration({hours: $delta})
  AND comment.creationDate > message2.creationDate + duration({hours: $delta})
  AND person1 <> person2 AND person1 <> person3 AND person2 <> person3
RETURN person1.id AS person1Id, count(DISTINCT message2) AS messageCount
ORDER BY messageCount DESC, person1Id ASC
LIMIT 100`, true)
}

// bi18: friend recommendation. Non-friends who share an interest tag with a start
// person, ranked by the number of mutual friends.
func bi18() *workload.WorkloadQuery {
	return biQuery("snb-bi18", `MATCH (tag:Tag {name: $tag}),
      (person1:Person {id: $personId})-[:KNOWS]-(mutualFriend:Person)-[:KNOWS]-(person2:Person)-[:HAS_INTEREST]->(tag)
WHERE person1 <> person2 AND NOT (person1)-[:KNOWS]-(person2)
RETURN person2.id AS person2Id, count(DISTINCT mutualFriend) AS mutualFriendCount
ORDER BY mutualFriendCount DESC, person2Id ASC
LIMIT 20`, true)
}

// bi19: interaction path between cities. The cheapest interaction-weighted KNOWS
// path between persons in two cities. Like BI15, the weighted shortest path is a
// procedure seam; the text carries the path and the per-edge interaction weight.
func bi19() *workload.WorkloadQuery {
	return biQuery("snb-bi19", `MATCH (city1:City {id: $city1Id})<-[:IS_LOCATED_IN]-(person1:Person),
      (city2:City {id: $city2Id})<-[:IS_LOCATED_IN]-(person2:Person)
WHERE person1 <> person2
WITH person1, person2
MATCH path = shortestPath((person1)-[:KNOWS*]-(person2))
WITH person1, person2,
     reduce(weight = 0.0, k IN relationships(path) |
       weight + round(40 - sqrt(toFloat(
         size([(pa)<-[:HAS_CREATOR]-(m1:Message)-[:REPLY_OF]-(m2:Message)-[:HAS_CREATOR]->(pb)
           WHERE pa = startNode(k) AND pb = endNode(k) | m1]))))) AS totalWeight
RETURN person1.id AS person1Id, person2.id AS person2Id, totalWeight
ORDER BY totalWeight ASC, person1Id ASC, person2Id ASC
LIMIT 20`, false)
}

// bi20: recruitment. The cheapest KNOWS path from each employee of a company to a
// target person, weighted by shared organisations between consecutive persons. The
// weighted shortest path is a procedure seam; the text carries the path shape.
func bi20() *workload.WorkloadQuery {
	return biQuery("snb-bi20", `MATCH (company:Company {name: $company})<-[:WORK_AT]-(person1:Person),
      (person2:Person {id: $person2Id})
WITH person1, person2
MATCH path = shortestPath((person1)-[:KNOWS*]-(person2))
WITH person1, person2,
     reduce(weight = 0, k IN relationships(path) |
       weight + size([(startNode(k))-[:STUDY_AT|WORK_AT]->(org:Organisation)<-[:STUDY_AT|WORK_AT]-(endNode(k)) | org])) AS totalWeight
RETURN person1.id AS person1Id, totalWeight
ORDER BY totalWeight ASC, person1Id ASC
LIMIT 20`, false)
}
