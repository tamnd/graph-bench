package micro

// This file registers "micro-write": the write microscope, one property
// update per query, on the same grid the read microscope runs on.
//
// The write is a self-assignment, `SET n.id = n.id`. That looks like a
// trick and is the point: it is stationary by construction, so the
// workload needs no Setup, no Teardown and no fresh ids, and it can be
// fired at a curated pool of real ids repetition after repetition
// without the graph drifting under the measurement. What it costs an
// engine is the whole write path anyway — parse, plan, match the row,
// stage the change, commit the log, and whatever the storage does to
// make the new value readable — because none of these engines look at
// the value being written and decide to skip the work.
//
// The property is id because the synthetic node table has no other one:
// the canonical node CSV is "id:ID,:LABEL" and the generators add
// nothing. On an engine that indexes the lookup key that means the
// number includes index maintenance, which is disclosed here rather than
// hidden: it is the same statement on every engine, and an engine that
// keeps an index for the read is entitled to pay for it on the write.
//
// Fidelity is "harness-native" (spec 07 §6), the same as the rest of the
// micro family: this query mirrors no external standard.

import (
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

func init() {
	workload.Register(microWrite)
}

// microWrite is the write family on the grid, drawing its targets from
// the same point-read pool the read family uses, so a write latency and
// a read latency on this dataset are answers about the same rows.
var microWrite = &workload.Workload{
	Name:     "micro-write",
	Title:    "Micro-benchmarks: single property updates on a grid",
	Family:   "micro",
	Dataset:  "grid-100x100",
	Fidelity: "harness-native",
	Queries:  []*workload.Query{setPropQuery},
}

// setPropQuery is the property update: find one node by id and write one
// cell. It is the cheapest write an engine does, the floor of the write
// latency distribution, and the counterpart of micro-point.
//
// The post-condition asks whether the row is still there under the id it
// was found by, which is what a self-assignment has to leave behind. A
// write that landed somewhere else, or that dropped the row, answers
// false or answers nothing.
var setPropQuery = &workload.Query{
	ID:      "micro-set",
	Class:   engine.Write,
	PoolKey: pointKey,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (n:Node {id: $id}) SET n.id = n.id`,
		engine.KuzuCy: `MATCH (n:Node {id: CAST($id AS INT64)}) SET n.id = n.id`,
		engine.ZuQL:   `MATCH (n:node {id: $id}) SET n.id = n.id`,
	},
	PostCondition: `MATCH (n:Node {id: $id}) WITH count(n) AS c RETURN c = 1 AS ok`,
	PostConditions: map[engine.Dialect]string{
		engine.KuzuCy: `MATCH (n:Node {id: CAST($id AS INT64)}) WITH count(n) AS c RETURN c = 1 AS ok`,
		engine.ZuQL:   `MATCH (n:node {id: $id}) WITH count(n) AS c RETURN c = 1 AS ok`,
	},
	AutocommitOK: true,
}
