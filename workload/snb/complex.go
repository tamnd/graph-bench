package snb

import (
	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
)

func init() {
	workload.Register(complexWorkload)
}

// complexWorkload is the "snb-complex" workload: the six curated SNB Interactive
// complex reads that finish in the 1-500 ms band at SF1 and fit a CI runner.
// The heavier IC3/IC4/IC5/IC7/IC10/IC12/IC13/IC14 are deferred to the
// controlled-machine analytical tier. All six are class Traversal.
var complexWorkload = &workload.Workload{
	Name:    "snb-complex",
	Title:   "SNB Interactive curated complex reads IC1/IC2/IC6/IC8/IC9/IC11 (isolated)",
	Dataset: "snb-sf1",

	Queries: []*workload.WorkloadQuery{
		ic1(), ic2(), ic6(), ic8(), ic9(), ic11(),
	},
}

// ic1: friends with a given first name to three hops, with profile.
// Bounded variable-length KNOWS traversal, distinct-by-distance, fan-out of
// optional one-hop fetches for city, universities, and companies.
func ic1() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-ic1",
		Class: target.Traversal,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (p:Person {id: $personId}), (friend:Person {firstName: $firstName})
WHERE NOT p = friend
WITH p, friend
MATCH path = shortestPath((p)-[:KNOWS*1..3]-(friend))
WITH min(length(path)) AS distance, friend
ORDER BY distance ASC, friend.lastName ASC, toInteger(friend.id) ASC
LIMIT 20
MATCH (friend)-[:IS_LOCATED_IN]->(friendCity:Place)
OPTIONAL MATCH (friend)-[studyAt:STUDY_AT]->(uni:Organisation)-[:IS_PART_OF]->(uniCity:Place)
OPTIONAL MATCH (friend)-[workAt:WORK_AT]->(company:Organisation)-[:IS_PART_OF]->(companyCountry:Place)
RETURN
    friend.id AS personId,
    friend.lastName AS personLastName,
    distance,
    friend.birthday AS personBirthday,
    friend.creationDate AS personCreationDate,
    friend.gender AS personGender,
    friend.browserUsed AS personBrowserUsed,
    friend.locationIP AS personLocationIP,
    collect(DISTINCT [uni.name, studyAt.classYear, uniCity.name]) AS universities,
    collect(DISTINCT [company.name, workAt.workFrom, companyCountry.name]) AS companies,
    friendCity.name AS cityName
ORDER BY distance ASC, friend.lastName ASC, toInteger(friend.id) ASC`,
		},
		Params: workload.NewPool(nil), // {personId, firstName} pairs from curated person pool
		Reference: workload.RefStrategy{
			Compare: workload.CompareSpec{
				Ordered:   true,
				CoerceNum: false,
			},
			Compute: nil,
		},
	}
}

// ic2: a friend's twenty most recent messages before a date.
// One-hop KNOWS, temporal filter on creationDate, top-k ordered result.
func ic2() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-ic2",
		Class: target.Traversal,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (p:Person {id: $personId})-[:KNOWS]-(friend:Person),
      (friend)<-[:HAS_CREATOR]-(message:Message)
WHERE message.creationDate < $maxDate
WITH friend, message
ORDER BY message.creationDate DESC, message.id ASC
LIMIT 20
RETURN
    friend.id AS personId,
    friend.firstName AS personFirstName,
    friend.lastName AS personLastName,
    message.id AS messageId,
    coalesce(message.content, message.imageFile) AS messageContent,
    message.creationDate AS messageCreationDate
ORDER BY messageCreationDate DESC, messageId ASC`,
		},
		Params: workload.NewPool(nil), // {personId, maxDate} from curated message-window pool
		Reference: workload.RefStrategy{
			Compare: workload.CompareSpec{
				Ordered:   true,
				CoerceNum: false,
			},
			Compute: nil,
		},
	}
}

// ic6: tag co-occurrence among friends-of-friends.
// Two-hop KNOWS expansion, join to messages on a given tag, collect other tags
// on those messages, top-k by co-occurrence count.
func ic6() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-ic6",
		Class: target.Traversal,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (knownTag:Tag {name: $tagName})<-[:HAS_TAG]-(message:Message)
      -[:HAS_CREATOR]->(friend:Person)-[:KNOWS*1..2]-(p:Person {id: $personId})
WHERE NOT p = friend
MATCH (message)-[:HAS_TAG]->(commonTag:Tag)
WHERE NOT commonTag = knownTag
WITH commonTag, count(message) AS tagCount
ORDER BY tagCount DESC, commonTag.name ASC
LIMIT 10
RETURN commonTag.name AS tagName, tagCount`,
		},
		Params: workload.NewPool(nil), // {personId, tagName} from curated tag-cooccurrence pool
		Reference: workload.RefStrategy{
			Compare: workload.CompareSpec{
				Ordered:   true, // ORDER BY tagCount DESC, tagName ASC is stable for top-10
				CoerceNum: true, // count returns int on some engines, float on others
			},
			Compute: nil,
		},
	}
}

// ic8: twenty most recent replies to a person's messages.
// One-hop HAS_CREATOR to the person's messages, one-hop REPLY_OF to comments,
// then the comment's author. Top-k ordered by creation date.
func ic8() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-ic8",
		Class: target.Traversal,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (p:Person {id: $personId})<-[:HAS_CREATOR]-(message:Message)
      <-[:REPLY_OF]-(comment:Comment)-[:HAS_CREATOR]->(author:Person)
WITH author, comment
ORDER BY comment.creationDate DESC, comment.id ASC
LIMIT 20
RETURN
    author.id AS personId,
    author.firstName AS personFirstName,
    author.lastName AS personLastName,
    comment.creationDate AS commentCreationDate,
    comment.id AS commentId,
    comment.content AS commentContent
ORDER BY commentCreationDate DESC, commentId ASC`,
		},
		Params: workload.NewPool(nil), // {personId} from curated person pool
		Reference: workload.RefStrategy{
			Compare: workload.CompareSpec{
				Ordered:   true,
				CoerceNum: false,
			},
			Compute: nil,
		},
	}
}

// ic9: twenty most recent messages from the two-hop social neighborhood before
// a date. Two-hop KNOWS (excluding the start person), temporal filter, top-k.
func ic9() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-ic9",
		Class: target.Traversal,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (p:Person {id: $personId})-[:KNOWS*1..2]-(friend:Person)
WHERE NOT p = friend
WITH DISTINCT friend
MATCH (friend)<-[:HAS_CREATOR]-(message:Message)
WHERE message.creationDate < $maxDate
WITH friend, message
ORDER BY message.creationDate DESC, message.id ASC
LIMIT 20
RETURN
    friend.id AS personId,
    friend.firstName AS personFirstName,
    friend.lastName AS personLastName,
    message.id AS messageId,
    coalesce(message.content, message.imageFile) AS messageContent,
    message.creationDate AS messageCreationDate
ORDER BY messageCreationDate DESC, messageId ASC`,
		},
		Params: workload.NewPool(nil), // {personId, maxDate} from curated message-window pool
		Reference: workload.RefStrategy{
			Compare: workload.CompareSpec{
				Ordered:   true,
				CoerceNum: false,
			},
			Compute: nil,
		},
	}
}

// ic11: friends and friends-of-friends who work at companies in a given country
// before a given year. Two-hop KNOWS, organization join, country filter, work-year
// filter.
func ic11() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "snb-ic11",
		Class: target.Traversal,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (p:Person {id: $personId})-[:KNOWS*1..2]-(friend:Person)
WHERE NOT p = friend
WITH DISTINCT friend
MATCH (friend)-[workAt:WORK_AT]->(company:Organisation)-[:IS_PART_OF]->(country:Place)
WHERE country.name = $countryName
  AND workAt.workFrom < $workFromYear
RETURN
    friend.id AS personId,
    friend.firstName AS personFirstName,
    friend.lastName AS personLastName,
    company.name AS organizationName,
    workAt.workFrom AS organizationWorkFromYear
ORDER BY workAt.workFrom ASC, friend.id ASC, company.name DESC
LIMIT 10`,
		},
		Params: workload.NewPool(nil), // {personId, countryName, workFromYear} from curated pool
		Reference: workload.RefStrategy{
			Compare: workload.CompareSpec{
				Ordered:   true,
				CoerceNum: false,
			},
			Compute: nil,
		},
	}
}
