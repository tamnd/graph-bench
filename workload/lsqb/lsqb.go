// Package lsqb holds the nine Labelled Subgraph Query Benchmark queries over the
// SNB schema, registered as the "lsqb" workload. LSQB queries run in isolation
// (no Mix), each returning a count of pattern matches, validated against an
// engine-independent count reference. See notes/Spec/2060/bench/05-workloads.md
// section 4 for the full design and the per-query rationale.
//
// The nine queries cover the spectrum from tree-shaped joins (Q1-Q4, where join
// ordering is the bottleneck) to cyclic patterns (Q5-Q7, Q9, where worst-case-
// optimal joins beat binary plans) to shared-substructure (Q8, where
// factorization avoids redundant work). They are the standards-anchored version
// of the WCOJ and factorization micro-benchmarks, on real SNB labels.
package lsqb

import (
	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
)

func init() {
	workload.Register(lsqbWorkload)
}

var lsqbWorkload = &workload.Workload{
	Name:    "lsqb",
	Title:   "LSQB subgraph queries (Q1-Q9) over the SNB schema",
	Dataset: "snb-sf1", // resolved by dataset/ldbc for production runs

	// No Mix: all nine queries run in isolation so a regression is attributed to
	// one query, not lost in a blend.
	Queries: []*workload.WorkloadQuery{
		q1(), q2(), q3(), q4(), q5(), q6(), q7(), q8(), q9(),
	},
}

// q1: a tree: person, their city, their university, the country.
// Probes tree join ordering; no cycle.
func q1() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "lsqb-q1",
		Class: target.Subgraph,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (p:Person)-[:IS_LOCATED_IN]->(:City)-[:IS_PART_OF]->(:Country),
      (p)-[:STUDY_AT]->(:University)
RETURN count(*) AS cnt`,
		},
		Params:    workload.NewFixed(nil), // no parameters: counts the whole graph
		Reference: countRef(),
	}
}

// q2: a message, its creator, the creator's city, a tag on the message.
// A four-way tree with two fan-outs from the message node.
func q2() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "lsqb-q2",
		Class: target.Subgraph,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (m:Message)-[:HAS_CREATOR]->(p:Person)-[:IS_LOCATED_IN]->(:City),
      (m)-[:HAS_TAG]->(:Tag)
RETURN count(*) AS cnt`,
		},
		Params:    workload.NewFixed(nil),
		Reference: countRef(),
	}
}

// q3: a forum, its moderator, a member, a message the member posted in the forum.
// Probes a longer tree with a container join.
func q3() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "lsqb-q3",
		Class: target.Subgraph,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (f:Forum)-[:HAS_MODERATOR]->(:Person),
      (f)-[:HAS_MEMBER]->(p:Person),
      (f)-[:CONTAINER_OF]->(m:Message)-[:HAS_CREATOR]->(p)
RETURN count(*) AS cnt`,
		},
		Params:    workload.NewFixed(nil),
		Reference: countRef(),
	}
}

// q4: a message, its tag, the tag's class, the creator's country.
// A deeper tree with two label hierarchies; probes multi-level join chains.
func q4() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "lsqb-q4",
		Class: target.Subgraph,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (m:Message)-[:HAS_TAG]->(:Tag)-[:HAS_TYPE]->(:TagClass),
      (m)-[:HAS_CREATOR]->(:Person)-[:IS_LOCATED_IN]->(:City)-[:IS_PART_OF]->(:Country)
RETURN count(*) AS cnt`,
		},
		Params:    workload.NewFixed(nil),
		Reference: countRef(),
	}
}

// q5: a friendship triangle (three persons mutually knowing each other).
// The canonical 3-clique; the quintessential WCOJ query.
func q5() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "lsqb-q5",
		Class: target.Subgraph,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (a:Person)-[:KNOWS]-(b:Person)-[:KNOWS]-(c:Person)-[:KNOWS]-(a)
RETURN count(*) AS cnt`,
		},
		Params:    workload.NewFixed(nil),
		Reference: countRefOracle("lsqb-q5"),
	}
}

// q6: a triangle of persons who each created a message with a shared tag.
// Cycle plus content joins; mixed WCOJ and binary.
func q6() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "lsqb-q6",
		Class: target.Subgraph,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (a:Person)-[:KNOWS]-(b:Person)-[:KNOWS]-(c:Person)-[:KNOWS]-(a),
      (a)<-[:HAS_CREATOR]-(:Message)-[:HAS_TAG]->(t:Tag),
      (b)<-[:HAS_CREATOR]-(:Message)-[:HAS_TAG]->(t),
      (c)<-[:HAS_CREATOR]-(:Message)-[:HAS_TAG]->(t)
RETURN count(*) AS cnt`,
		},
		Params:    workload.NewFixed(nil),
		Reference: countRef(),
	}
}

// q7: a four-cycle of persons (a knows b knows c knows d knows a).
// A larger cycle; probes WCOJ generalization beyond 3-clique.
func q7() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "lsqb-q7",
		Class: target.Subgraph,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (a:Person)-[:KNOWS]-(b:Person)-[:KNOWS]-(c:Person)-[:KNOWS]-(d:Person)-[:KNOWS]-(a)
RETURN count(*) AS cnt`,
		},
		Params:    workload.NewFixed(nil),
		Reference: countRefOracle("lsqb-q7"),
	}
}

// q8: a person, two of their messages, a tag shared by both messages.
// A shared substructure (the person and tag appear twice in the pattern);
// probes factorization.
func q8() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "lsqb-q8",
		Class: target.Subgraph,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (p:Person)<-[:HAS_CREATOR]-(m1:Message)-[:HAS_TAG]->(t:Tag),
      (p)<-[:HAS_CREATOR]-(m2:Message)-[:HAS_TAG]->(t)
WHERE m1 <> m2
RETURN count(*) AS cnt`,
		},
		Params:    workload.NewFixed(nil),
		Reference: countRefOracle("lsqb-q8"),
	}
}

// q9: a dense cyclic pattern with a shared forum and shared tag.
// A KNOWS triangle whose members share a Forum via HAS_MEMBER and share a Tag
// via the tag attached to a message each created. The hardest LSQB query:
// cycle + multiple binary joins on top.
func q9() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "lsqb-q9",
		Class: target.Subgraph,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `MATCH (a:Person)-[:KNOWS]-(b:Person)-[:KNOWS]-(c:Person)-[:KNOWS]-(a),
      (f:Forum)-[:HAS_MEMBER]->(a),
      (f)-[:HAS_MEMBER]->(b),
      (f)-[:HAS_MEMBER]->(c),
      (a)<-[:HAS_CREATOR]-(ma:Message)-[:HAS_TAG]->(t:Tag),
      (b)<-[:HAS_CREATOR]-(mb:Message)-[:HAS_TAG]->(t),
      (c)<-[:HAS_CREATOR]-(mc:Message)-[:HAS_TAG]->(t)
RETURN count(*) AS cnt`,
		},
		Params:    workload.NewFixed(nil),
		Reference: countRef(),
	}
}

// countRef returns a RefStrategy for LSQB count queries with no independent
// oracle yet. Every LSQB query returns a single integer row (count(*) AS cnt),
// and validation compares the engine's count under numeric coercion so an engine
// that returns the count as a float still validates.
//
// Compute is nil here, so validation is skipped. The tree-shaped joins (Q1-Q4)
// and the dense composite queries (Q6, Q9) still carry no reference; they get a
// CSV-based oracle once their counting routines are wired into CountOracle.
func countRef() workload.RefStrategy {
	return workload.RefStrategy{
		Compare: workload.CompareSpec{
			Ordered:   false,
			CoerceNum: true, // some engines return count(*) as float64
		},
		Compute: nil,
	}
}

// countRefOracle returns a RefStrategy whose Compute runs the engine-independent
// CountOracle for the given query id. It is used by the queries with a trusted
// reference routine (the 3-clique, the four-cycle, the shared substructure), so
// a run validates the engine's count against the CSV oracle rather than skipping
// the check. The single count(*) AS cnt row is wrapped in the canonical answer.
func countRefOracle(queryID string) workload.RefStrategy {
	r := countRef()
	r.Compute = func(ds target.Dataset, _ target.Params) (*target.Answer, error) {
		n, err := CountOracle(queryID, ds)
		if err != nil {
			return nil, err
		}
		return &target.Answer{
			Columns: []string{"cnt"},
			Rows:    [][]target.Value{{n}},
		}, nil
	}
	return r
}
