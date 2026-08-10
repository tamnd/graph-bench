// Package lsqb registers the LSQB-shaped join workload (spec 07 §3): nine
// parameterless count(*) queries whose only work is multi-way join
// evaluation — no filtering, no property access, no result shaping — so they
// isolate the optimizer's join-ordering and the executor's join throughput.
//
// The original LSQB runs over the LDBC SNB schema. graph-bench's smoke-scale
// social generator emits a reduced schema (Person/Post/Forum with
// KNOWS/HAS_CREATOR/LIKES/HAS_MEMBER/CONTAINER_OF), so the nine queries here
// keep the v1 join shapes — trees, diamonds, triangles, and a four-cycle of
// increasing arity — but bind them to that schema. Fidelity is therefore
// "spec-following", not "faithful": the shapes and the count-only discipline
// are LSQB's, the labels are ours. No anti-join (q6/q9 in upstream LSQB use
// negation) appears because v1 carried none.
//
// Every query is Class Aggregation per the harness taxonomy: a single
// aggregate row over an unbounded match set. References come from the
// straight-line counting oracles in oracle.go, computed off the canonical
// CSV.
package lsqb

import (
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

func init() {
	workload.Register(lsqbWorkload())
}

// lsqbWorkload assembles the workload: nine fixed (parameterless) count
// queries over the social-1k recipe, verified at full strength (every query
// has exactly one parameter binding, so one verification covers it).
func lsqbWorkload() *workload.Workload {
	return &workload.Workload{
		Name:     "lsqb",
		Title:    "LSQB: labelled subgraph join counts",
		Family:   "lsqb",
		Dataset:  "social-1k",
		Fidelity: "spec-following",
		Queries: []*workload.Query{
			countQuery("lsqb-q1",
				// Tree: containment chain extended by the creator's likes.
				`MATCH (f:Forum)-[:CONTAINER_OF]->(m:Post)-[:HAS_CREATOR]->(p:Person)-[:LIKES]->(:Post)
RETURN count(*) AS cnt`),
			countQuery("lsqb-q2",
				// Post hub with two branches: creator's friendships and likers.
				`MATCH (m:Post)-[:HAS_CREATOR]->(p:Person)-[:KNOWS]->(:Person), (m)<-[:LIKES]-(:Person)
RETURN count(*) AS cnt`),
			countQuery("lsqb-q3",
				// Diamond: the member is also the creator of a contained post.
				`MATCH (f:Forum)-[:HAS_MEMBER]->(p:Person), (f)-[:CONTAINER_OF]->(m:Post)-[:HAS_CREATOR]->(p)
RETURN count(*) AS cnt`),
			countQuery("lsqb-q4",
				// Deeper tree: friend-of-friend chain plus likes and containment.
				`MATCH (m:Post)-[:HAS_CREATOR]->(:Person)-[:KNOWS]->(:Person)-[:KNOWS]->(:Person), (m)<-[:LIKES]-(:Person), (m)<-[:CONTAINER_OF]-(:Forum)
RETURN count(*) AS cnt`),
			countQuery("lsqb-q5",
				// The undirected KNOWS triangle: the cyclic-join stress case.
				`MATCH (a:Person)-[:KNOWS]-(b:Person)-[:KNOWS]-(c:Person)-[:KNOWS]-(a)
RETURN count(*) AS cnt`),
			countQuery("lsqb-q6",
				// Triangle joined with each member's post in a shared forum.
				`MATCH (a:Person)-[:KNOWS]-(b:Person)-[:KNOWS]-(c:Person)-[:KNOWS]-(a),
      (f:Forum)-[:CONTAINER_OF]->(ma:Post)-[:HAS_CREATOR]->(a),
      (f)-[:CONTAINER_OF]->(mb:Post)-[:HAS_CREATOR]->(b),
      (f)-[:CONTAINER_OF]->(mc:Post)-[:HAS_CREATOR]->(c)
RETURN count(*) AS cnt`),
			countQuery("lsqb-q7",
				// The undirected KNOWS four-cycle: the longer cyclic join.
				//
				// The four relationships are named and required pairwise
				// distinct rather than left anonymous, because engines disagree
				// about what an anonymous cyclic pattern means. Cypher specifies
				// relationship isomorphism — no relationship may be reused
				// within a MATCH — and Neo4j enforces it; Kuzu evaluates the
				// same text as a join with no such restriction, which counts
				// every closed four-walk, including a-b-a-d-a traversing one
				// relationship twice. On social-1k that is 6828772 against
				// 5872264: a 16% spread that is a semantic disagreement, not an
				// engine being wrong, and comparing the two numbers would be
				// comparing answers to different questions. Spelling the
				// constraint out makes the query mean one thing everywhere.
				//
				// Only the four-cycle needs this. A closed three-walk in a
				// loop-free graph is always a triangle, so q5/q6/q9 cannot
				// admit a repeated relationship and the two semantics coincide
				// there — which is why they agree today and get no predicate.
				`MATCH (a:Person)-[r1:KNOWS]-(b:Person)-[r2:KNOWS]-(c:Person)-[r3:KNOWS]-(d:Person)-[r4:KNOWS]-(a)
WHERE r1 <> r2 AND r1 <> r3 AND r1 <> r4 AND r2 <> r3 AND r2 <> r4 AND r3 <> r4
RETURN count(*) AS cnt`),
			countQuery("lsqb-q8",
				// Shared substructure: two distinct posts by one person in one forum.
				`MATCH (f:Forum)-[:CONTAINER_OF]->(m1:Post)-[:HAS_CREATOR]->(p:Person),
      (f)-[:CONTAINER_OF]->(m2:Post)-[:HAS_CREATOR]->(p)
WHERE m1 <> m2
RETURN count(*) AS cnt`),
			countQuery("lsqb-q9",
				// Triangle with a shared forum membership and a common liked post.
				`MATCH (a:Person)-[:KNOWS]-(b:Person)-[:KNOWS]-(c:Person)-[:KNOWS]-(a),
      (f:Forum)-[:HAS_MEMBER]->(a), (f)-[:HAS_MEMBER]->(b), (f)-[:HAS_MEMBER]->(c),
      (a)-[:LIKES]->(m:Post), (b)-[:LIKES]->(m), (c)-[:LIKES]->(m)
RETURN count(*) AS cnt`),
		},
		Analytics:       true,
		ValidationScale: "social-1k",
	}
}

// countQuery builds one parameterless counting query. Cypher-only: the texts
// use no parameters and no engine-specific types, so every engine reaches
// them through its Cypher fallback (ladybug's Kuzu dialect accepts this
// subset unchanged; zu has no primitive for these shapes, so its ZuQL slot
// stays empty by design).
func countQuery(id, cypher string) *workload.Query {
	return &workload.Query{
		ID:    id,
		Class: engine.Aggregation,
		Texts: map[engine.Dialect]string{
			engine.Cypher: cypher,
		},
		Params: workload.Fixed{},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, _ workload.Params) (*workload.Answer, error) {
				n, err := CountOracle(id, ds)
				if err != nil {
					return nil, err
				}
				return &workload.Answer{
					Columns: []string{"cnt"},
					Rows:    [][]engine.Value{{n}},
				}, nil
			},
			Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
		},
	}
}
