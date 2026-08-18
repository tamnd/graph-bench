package micro

// This file registers "micro-write": the write microscope, one property
// update per query, the counterpart of the point read.
//
// The write is a self-assignment, `SET o.version = o.version`. That looks
// like a trick and is the point: it is stationary by construction, so the
// workload needs no Setup, no Teardown and no fresh ids, and it can be
// fired at a curated pool of real ids repetition after repetition without
// the graph drifting under the measurement. What it costs an engine is
// the whole write path anyway, which is parse, plan, match the row, stage
// the change, commit the log, and whatever the storage does to make the
// new value readable, because none of these engines look at the value
// being written and decide to skip the work.
//
// It runs on the linkbench object graph rather than on the grid the read
// microscope uses, and the reason is worth writing down. The grid's node
// file is "id:ID,:LABEL" and has no other column, so the only property
// there to write is the one the row is found by, and an engine that keys
// its node table on that column refuses the statement outright: Ladybug
// answers "Cannot set property id in table Node because it is used as
// primary key". The object graph has a plain INT64 version column beside
// its id, which is what a write microscope wants: found by the key,
// written somewhere else, so the number measures the write rather than
// the engine's opinion about keys.
//
// The column being written is not the same one on every engine, and that
// is a disclosure rather than an oversight. zu loads a dataset through
// `zu copy`, which builds one node table named node holding the keys and
// one rel table named edge holding the properties of the single rel
// table, so the only node column zu has on this dataset is id and the
// only self-assignment zu can make is to it. Ladybug and Neo4j load the
// canonical node file with every column, so they write version and leave
// their key alone. Both statements are the same shape, which is find one
// node by its key and write one cell of it, and both pay the engine's
// whole write path; what differs is that zu's cell is the one its lookup
// key lives in, so an engine that maintains a key structure on write
// pays for that here too.
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

// microWrite is the write family, drawing its targets from the flat pool
// of existing ids the point read draws from, so a write latency and a
// read latency on this dataset are answers about the same rows.
var microWrite = &workload.Workload{
	Name:     "micro-write",
	Title:    "Micro-benchmarks: single property updates",
	Family:   "micro",
	Dataset:  "lb-10k",
	Fidelity: "harness-native",
	Queries:  []*workload.Query{setPropQuery},
}

// setPropQuery is the property update: find one node by id and write one
// cell. It is the cheapest write an engine does, the floor of the write
// latency distribution.
//
// The post-condition asks whether the row is still there under the id it
// was found by, which is what a self-assignment has to leave behind. A
// write that landed on another row, or that dropped the one it found,
// answers false or answers nothing.
var setPropQuery = &workload.Query{
	ID:      "micro-set",
	Class:   engine.Write,
	PoolKey: pointKey,
	Texts: map[engine.Dialect]string{
		engine.Cypher: `MATCH (o:Obj {id: $id}) SET o.version = o.version`,
		engine.KuzuCy: `MATCH (o:Obj {id: CAST($id AS INT64)}) SET o.version = o.version`,
		engine.ZuQL:   `MATCH (o:Obj {id: $id}) SET o.version = o.version`,
	},
	PostCondition: `MATCH (o:Obj {id: $id}) WITH count(o) AS c RETURN c = 1 AS ok`,
	PostConditions: map[engine.Dialect]string{
		engine.KuzuCy: `MATCH (o:Obj {id: CAST($id AS INT64)}) WITH count(o) AS c RETURN c = 1 AS ok`,
		engine.ZuQL:   `MATCH (o:Obj {id: $id}) WITH count(o) AS c RETURN c = 1 AS ok`,
	},
	AutocommitOK: true,
}
