package micro

import (
	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
)

func init() {
	workload.Register(microWrite)
}

// microWrite is the write family on an empty or lightly-loaded graph. The write
// queries use a scratch label (:Bench) that does not exist in the loaded dataset,
// so they do not mutate the benchmark graph and teardown can drop the label
// without affecting anything the read queries measured. The dataset field is left
// empty here; the harness (M4) supplies a target that has the loaded graph the
// writes run on top of. In v1 this is the SNB graph; in the CI smoke gate it is
// the grid graph since SNB is not loaded in the fast path.
//
// Write validation is a post-condition count, not a per-query returned row (spec
// doc 05 section 2.8): after the write phase the harness runs
// "MATCH (n:Bench) RETURN count(n)" and checks that it equals the arithmetic of
// creates minus deletes. The RefStrategy.Compute fields below are nil, which is
// the sentinel the harness recognizes as post-condition validation. A shipped
// write query with nil Compute but no registered PostCondition check is a review
// failure, not a silent pass (F1).
var microWrite = &workload.Workload{
	Name:    "micro-write",
	Title:   "Micro-benchmarks for write throughput (single, batch, relationship, delete)",
	Dataset: "",
	Queries: []*workload.WorkloadQuery{
		writeNodeQuery,
		writeNodeBatchQuery,
		writeRelQuery,
		writeDeleteQuery,
	},
}

// writeNodeQuery is the single-node autocommit create: the floor of write latency.
// Parameters: id (unique int64 per call, monotonic from a run-scoped counter) and
// val (a random int64). The harness generates non-colliding ids so two runs on the
// same engine never conflict.
var writeNodeQuery = &workload.WorkloadQuery{
	ID:    "micro-write-node",
	Class: target.Write,
	Texts: map[workload.Dialect]string{
		workload.Cypher: `CREATE (n:Bench {id: $id, val: $val})`,
	},
	// RefStrategy.Compute is nil: post-condition count after the write phase.
}

// writeNodeBatchQuery is the batched create: N nodes in one transaction, the
// group-commit showcase. The default batch size is 1000 rows per call. Parameters:
// rows (a list of {id, val} maps). The id values are monotonically increasing from
// the run-scoped counter, so a batch of 1000 produces ids in a contiguous range.
var writeNodeBatchQuery = &workload.WorkloadQuery{
	ID:    "micro-write-node-batch",
	Class: target.Write,
	Texts: map[workload.Dialect]string{
		workload.Cypher: `UNWIND $rows AS r CREATE (n:Bench {id: r.id, val: r.val})`,
	},
	// RefStrategy.Compute is nil: post-condition count after the write phase.
}

// writeRelQuery creates a relationship between two existing Bench nodes. Parameters:
// src and dst are ids of nodes the create phase already wrote. The harness picks
// (src, dst) pairs from the set of ids it issued during the create phase.
var writeRelQuery = &workload.WorkloadQuery{
	ID:    "micro-write-rel",
	Class: target.Write,
	Texts: map[workload.Dialect]string{
		workload.Cypher: `MATCH (a:Bench {id: $src}), (b:Bench {id: $dst}) CREATE (a)-[:BR {w: $w}]->(b)`,
	},
	// RefStrategy.Compute is nil: post-condition count after the write phase.
}

// writeDeleteQuery deletes one Bench node and all its incident edges via DETACH
// DELETE. Parameter: id of a node the create phase wrote (the harness tracks
// which ids are alive and draws from them so a delete always hits a real node).
var writeDeleteQuery = &workload.WorkloadQuery{
	ID:    "micro-write-delete",
	Class: target.Write,
	Texts: map[workload.Dialect]string{
		workload.Cypher: `MATCH (n:Bench {id: $id}) DETACH DELETE n`,
	},
	// RefStrategy.Compute is nil: post-condition count after the write phase.
}

// PostConditionText is the count query the harness runs after the write phase to
// check that the Bench label node count equals the arithmetic of creates minus
// deletes. It is exported so the measurement layer (M4) can issue it after the
// write phase completes and validate it against the expected count.
const PostConditionText = `MATCH (n:Bench) RETURN count(n) AS n`

// WritePostCondition returns the expected post-condition reference for the write
// phase: a single-row, single-column answer with the node count that should be
// present after created nodes were created and deleted nodes were removed. The
// harness calls this with the counts it tracked and compares the count query
// result against it.
func WritePostCondition(created, deleted int64) *target.Answer {
	if created < deleted {
		deleted = created
	}
	return &target.Answer{
		Columns: []string{"n"},
		Rows:    [][]target.Value{{created - deleted}},
	}
}

// TeardownText drops the scratch :Bench label so subsequent runs start clean.
// The harness issues this at Teardown time, after measurements are complete.
const TeardownText = `MATCH (n:Bench) DETACH DELETE n`

// validateWritePostCondition is a package-level helper the test uses to exercise
// the post-condition arithmetic without needing a real engine.
func validateWritePostCondition(created, deleted, actual int64) error {
	want := WritePostCondition(created, deleted)
	got := &target.Answer{
		Columns: []string{"n"},
		Rows:    [][]target.Value{{actual}},
	}
	return workload.Compare(got, want, workload.CompareSpec{Ordered: true, CoerceNum: true})
}

// ValidatePostCondition validates the actual node count after a write phase
// against the expected arithmetic. It returns nil when the counts match, or an
// error describing the divergence.
func ValidatePostCondition(created, deleted, actual int64) error {
	return validateWritePostCondition(created, deleted, actual)
}

