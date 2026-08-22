// Package linkbench implements the LinkBench-derived workload of spec
// 06-workloads-oltp.md §5: Meta's social-graph OLTP operation mix over
// the harness lb generator's object/association graph ("lb-10k"). The
// object model maps to one Obj node table and one typed LINK edge
// table; the ten operations and their mix percentages are the published
// LinkBench distribution from the spec table. Upstream LinkBench is a
// Java load driver, not a dataset, so the workload runs the same
// operation shapes on harness-generated data and is labeled fidelity
// "derived" (spec 06 §5, ADR-7).
//
// Link counts are computed by aggregation in the shared Cypher texts
// (count(l) over the (src, ltype) adjacency); an engine that maintains
// native counts would carry its own dialect text and disclose the
// modeling difference per the spec.
//
// zu carries its own text for eight of the ten operations. The reads
// and the object insert are the same query written in zuQL, and the
// three association writes differ only in that the association type is
// a WHERE predicate rather than an inline pattern property. The
// brackets are dialect-specific for the same reason: a creation is
// INSERT and not CREATE, and a post-condition that compares a count has
// to carry the count through a WITH, because zu takes an aggregate only
// as a bare projection item.
//
// The two that have no zu text are lb-update-node and lb-delete-node,
// and the reason is that a zu node id is the row offset the loader gave
// it, not a column an INSERT can choose. The scratch object those two
// operations work on therefore lands at whatever offset the store is
// next free at, and no statement written ahead of the run can name it.
// Matching it on its payload instead would put a scan of the whole
// object table inside the timed statement, which is not the point
// operation LinkBench measures, so they SKIP rather than report the
// wrong thing. lb-add-node is the one of the three that survives: the
// insert itself needs no id, and only its untimed post-condition and
// teardown look the object up by payload.
//
// The three association writes address the pairs (0,1), (0,2) and (0,3),
// which the generator leaves unlinked at every scale. That matters on zu
// beyond keeping the marker association unique: a rel table that stores
// edge properties refuses a second edge over a pair it already holds.
//
// Access skew: the curated pools (BuildPools) approximate LinkBench's
// hot-spot distribution by drawing the hottest sources of the power-law
// out-link distribution, pre-drawn deterministically so every engine
// sees the identical sequence.
package linkbench

import (
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

func init() {
	workload.Register(build())
}

// Write markers: ltypes 97-99 are outside the generator's 1..10 range
// and object ids 9000001+ are beyond any generated scale, so the write
// operations can address exactly their own data with literal
// post-conditions and teardowns while repetitions stay stationary.

func build() *workload.Workload {
	return &workload.Workload{
		Name:     "linkbench",
		Title:    "LinkBench-derived social-graph OLTP mix",
		Family:   "linkbench",
		Dataset:  "lb-10k",
		Fidelity: "derived",
		Queries: []*workload.Query{
			getNode(), getLink(), getLinks(), countLink(),
			addNode(), updateNode(), deleteNode(),
			addLink(), updateLink(), deleteLink(),
		},
		// The published LinkBench operation distribution (spec 06 §5).
		Mix: &workload.Mix{
			Weights: map[string]float64{
				"lb-get-node":    12.9,
				"lb-get-link":    0.5,
				"lb-get-links":   50.7,
				"lb-count-link":  4.9,
				"lb-add-node":    2.6,
				"lb-update-node": 7.4,
				"lb-delete-node": 1.0,
				"lb-add-link":    9.0,
				"lb-update-link": 8.0,
				"lb-delete-link": 3.0,
			},
			StreamKey: "",
		},
	}
}

// getNode is LinkBench get_node: object by id. [PointRead]
func getNode() *workload.Query {
	return &workload.Query{
		ID:      "lb-get-node",
		Class:   engine.PointRead,
		PoolKey: "lb-node",
		Texts: map[engine.Dialect]string{
			engine.Cypher: `MATCH (o:Obj {id: $id})
RETURN o.otype AS otype, o.version AS version, o.time AS time, o.payload AS payload`,
			engine.ZuQL: "MATCH (o:Obj {id: $id})\n" +
				"RETURN o.otype AS otype, o.version AS version, o.time AS `time`, o.payload AS payload",
		},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
				g, err := lbFor(ds)
				if err != nil {
					return nil, err
				}
				id, err := lpStr(p, "id")
				if err != nil {
					return nil, err
				}
				ans := &workload.Answer{Columns: []string{"otype", "version", "time", "payload"}}
				if o, ok := g.objs[id]; ok {
					ans.Rows = [][]engine.Value{{o.otype, o.version, o.time, o.payload}}
				}
				return ans, nil
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// getLink is LinkBench get_link: does the association (src, ltype, dst)
// exist. [PointRead]
func getLink() *workload.Query {
	return &workload.Query{
		ID:      "lb-get-link",
		Class:   engine.PointRead,
		PoolKey: "lb-link",
		Texts: map[engine.Dialect]string{
			engine.Cypher: `MATCH (:Obj {id: $src})-[l:LINK {ltype: $ltype}]->(:Obj {id: $dst})
RETURN count(l) AS found`,
			engine.ZuQL: `MATCH (:Obj {id: $src})-[l:LINK]->(:Obj {id: $dst})
WHERE l.ltype = $ltype
RETURN count(l) AS found`,
		},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
				g, err := lbFor(ds)
				if err != nil {
					return nil, err
				}
				src, ltype, err := srcLtype(p)
				if err != nil {
					return nil, err
				}
				dst, err := lpStr(p, "dst")
				if err != nil {
					return nil, err
				}
				var n int64
				for _, l := range g.out[src] {
					if l.ltype == ltype && l.dst == dst {
						n++
					}
				}
				return &workload.Answer{Columns: []string{"found"}, Rows: [][]engine.Value{{n}}}, nil
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// getLinks is LinkBench get_link_list: the (src, ltype) association
// range scan, newest first, limit 10000 (the spec table's 10k bound).
// The payload column joins the projection so the ORDER BY has an
// engine-independent total order (payloads are unique random strings,
// while dst ids would sort differently as strings versus integers).
// [Traversal]
func getLinks() *workload.Query {
	return &workload.Query{
		ID:      "lb-get-links",
		Class:   engine.Traversal,
		PoolKey: "lb-links",
		Texts: map[engine.Dialect]string{
			engine.Cypher: `MATCH (:Obj {id: $src})-[l:LINK]->(b:Obj)
WHERE l.ltype = $ltype
RETURN b.id AS dst, l.time AS time, l.payload AS payload
ORDER BY time DESC, payload ASC
LIMIT 10000`,
			engine.ZuQL: "MATCH (:Obj {id: $src})-[l:LINK]->(b:Obj)\n" +
				"WHERE l.ltype = $ltype\n" +
				"RETURN b.id AS dst, l.time AS `time`, l.payload AS payload\n" +
				"ORDER BY `time` DESC, payload ASC\n" +
				"LIMIT 10000",
		},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
				g, err := lbFor(ds)
				if err != nil {
					return nil, err
				}
				src, ltype, err := srcLtype(p)
				if err != nil {
					return nil, err
				}
				return &workload.Answer{
					Columns: []string{"dst", "time", "payload"},
					Rows:    g.linksList(src, ltype, 10000),
				}, nil
			},
			Compare: workload.CompareSpec{Ordered: true, CoerceNum: true},
		},
	}
}

// countLink is LinkBench count_link: the (src, ltype) association
// count, answered by degree aggregation. [PointRead per the spec table]
func countLink() *workload.Query {
	return &workload.Query{
		ID:      "lb-count-link",
		Class:   engine.PointRead,
		PoolKey: "lb-count",
		Texts: map[engine.Dialect]string{
			engine.Cypher: `MATCH (:Obj {id: $src})-[l:LINK]->(:Obj)
WHERE l.ltype = $ltype
RETURN count(l) AS n`,
			engine.ZuQL: `MATCH (:Obj {id: $src})-[l:LINK]->(:Obj)
WHERE l.ltype = $ltype
RETURN count(l) AS n`,
		},
		Reference: &workload.RefStrategy{
			Compute: func(ds engine.Dataset, p workload.Params) (*workload.Answer, error) {
				g, err := lbFor(ds)
				if err != nil {
					return nil, err
				}
				src, ltype, err := srcLtype(p)
				if err != nil {
					return nil, err
				}
				var n int64
				for _, l := range g.out[src] {
					if l.ltype == ltype {
						n++
					}
				}
				return &workload.Answer{Columns: []string{"n"}, Rows: [][]engine.Value{{n}}}, nil
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// addNode is LinkBench add_node. [Write]
func addNode() *workload.Query {
	return &workload.Query{
		ID:    "lb-add-node",
		Class: engine.Write,
		Texts: map[engine.Dialect]string{
			engine.Cypher: `CREATE (:Obj {id: $id, otype: $otype, version: $version, time: $time, payload: $payload})`,
			// No id: a zu node id is the row offset the store hands out,
			// so the object is created without one and found again by the
			// payload marker. The $id binding is left in place and unread,
			// which zu allows, so both dialects draw the same parameters.
			engine.ZuQL: `INSERT (:Obj {otype: $otype, version: $version, time: $time, payload: $payload})`,
		},
		Params: workload.Fixed{P: workload.Params{
			"id": int64(9000001), "otype": int64(1), "version": int64(0),
			"time": int64(0), "payload": "graph-bench",
		}},
		PostCondition: `MATCH (o:Obj {id: 9000001}) RETURN count(o) = 1`,
		PostConditions: map[engine.Dialect]string{
			engine.ZuQL: `MATCH (o:Obj) WHERE o.payload = 'graph-bench'
WITH count(o) AS c
RETURN c = 1 AS ok`,
		},
		Teardown: `MATCH (o:Obj {id: 9000001}) DETACH DELETE o`,
		Teardowns: map[engine.Dialect]string{
			engine.ZuQL: `MATCH (o:Obj) WHERE o.payload = 'graph-bench' DETACH DELETE o`,
		},
		AutocommitOK: true,
	}
}

// updateNode is LinkBench update_node: it bumps the version and swaps
// the payload of a scratch object the setup creates. No zu text: the
// setup cannot choose the object's id, so the timed statement has
// nothing to name it by. [Write]
func updateNode() *workload.Query {
	return &workload.Query{
		ID:    "lb-update-node",
		Class: engine.Write,
		Texts: map[engine.Dialect]string{
			engine.Cypher: `MATCH (o:Obj {id: $id})
SET o.payload = $payload, o.version = o.version + 1`,
		},
		Params: workload.Fixed{P: workload.Params{
			"id": int64(9000002), "payload": "updated",
		}},
		Setup:         `CREATE (:Obj {id: 9000002, otype: 1, version: 0, time: 0, payload: "seed"})`,
		PostCondition: `MATCH (o:Obj {id: 9000002}) RETURN o.payload = "updated" AND o.version = 1`,
		Teardown:      `MATCH (o:Obj {id: 9000002}) DETACH DELETE o`,
		AutocommitOK:  true,
	}
}

// deleteNode is LinkBench delete_node: it removes the scratch object
// the setup creates; the teardown is an idempotent sweep in case the
// operation itself failed. No zu text, for the reason updateNode gives.
// [Write]
func deleteNode() *workload.Query {
	return &workload.Query{
		ID:    "lb-delete-node",
		Class: engine.Write,
		Texts: map[engine.Dialect]string{
			engine.Cypher: `MATCH (o:Obj {id: $id}) DETACH DELETE o`,
		},
		Params: workload.Fixed{P: workload.Params{
			"id": int64(9000003),
		}},
		Setup:         `CREATE (:Obj {id: 9000003, otype: 1, version: 0, time: 0, payload: "seed"})`,
		PostCondition: `MATCH (o:Obj {id: 9000003}) RETURN count(o) = 0`,
		Teardown:      `MATCH (o:Obj {id: 9000003}) DETACH DELETE o`,
		AutocommitOK:  true,
	}
}

// addLink is LinkBench add_link: a marker association (ltype 99)
// between two existing objects. [Write]
func addLink() *workload.Query {
	return &workload.Query{
		ID:    "lb-add-link",
		Class: engine.Write,
		Texts: map[engine.Dialect]string{
			engine.Cypher: `MATCH (a:Obj {id: $src}), (b:Obj {id: $dst})
CREATE (a)-[:LINK {ltype: $ltype, time: $time, payload: $payload}]->(b)`,
			engine.ZuQL: `MATCH (a:Obj {id: $src}), (b:Obj {id: $dst})
INSERT (a)-[:LINK {ltype: $ltype, time: $time, payload: $payload}]->(b)`,
		},
		Params: workload.Fixed{P: workload.Params{
			"src": int64(0), "dst": int64(1), "ltype": int64(99),
			"time": int64(0), "payload": "graph-bench",
		}},
		PostCondition: `MATCH (:Obj {id: 0})-[l:LINK {ltype: 99}]->(:Obj {id: 1})
RETURN count(l) = 1`,
		PostConditions: map[engine.Dialect]string{
			engine.ZuQL: `MATCH (:Obj {id: 0})-[l:LINK]->(:Obj {id: 1})
WHERE l.ltype = 99
WITH count(l) AS c
RETURN c = 1 AS ok`,
		},
		Teardown: `MATCH (:Obj {id: 0})-[l:LINK {ltype: 99}]->(:Obj {id: 1})
DELETE l`,
		Teardowns: map[engine.Dialect]string{
			engine.ZuQL: `MATCH (:Obj {id: 0})-[l:LINK]->(:Obj {id: 1})
WHERE l.ltype = 99
DELETE l`,
		},
		AutocommitOK: true,
	}
}

// updateLink is LinkBench update_link: it swaps the payload of a marker
// association (ltype 98) the setup creates. [Write]
func updateLink() *workload.Query {
	return &workload.Query{
		ID:    "lb-update-link",
		Class: engine.Write,
		Texts: map[engine.Dialect]string{
			engine.Cypher: `MATCH (:Obj {id: $src})-[l:LINK {ltype: $ltype}]->(:Obj {id: $dst})
SET l.payload = $payload`,
			engine.ZuQL: `MATCH (:Obj {id: $src})-[l:LINK]->(:Obj {id: $dst})
WHERE l.ltype = $ltype
SET l.payload = $payload`,
		},
		Params: workload.Fixed{P: workload.Params{
			"src": int64(0), "dst": int64(2), "ltype": int64(98), "payload": "updated",
		}},
		Setup: `MATCH (a:Obj {id: 0}), (b:Obj {id: 2})
CREATE (a)-[:LINK {ltype: 98, time: 0, payload: "seed"}]->(b)`,
		Setups: map[engine.Dialect]string{
			engine.ZuQL: `MATCH (a:Obj {id: 0}), (b:Obj {id: 2})
INSERT (a)-[:LINK {ltype: 98, time: 0, payload: 'seed'}]->(b)`,
		},
		PostCondition: `MATCH (:Obj {id: 0})-[l:LINK {ltype: 98}]->(:Obj {id: 2})
RETURN l.payload = "updated"`,
		PostConditions: map[engine.Dialect]string{
			engine.ZuQL: `MATCH (:Obj {id: 0})-[l:LINK]->(:Obj {id: 2})
WHERE l.ltype = 98
RETURN l.payload = 'updated' AS ok`,
		},
		Teardown: `MATCH (:Obj {id: 0})-[l:LINK {ltype: 98}]->(:Obj {id: 2})
DELETE l`,
		Teardowns: map[engine.Dialect]string{
			engine.ZuQL: `MATCH (:Obj {id: 0})-[l:LINK]->(:Obj {id: 2})
WHERE l.ltype = 98
DELETE l`,
		},
		AutocommitOK: true,
	}
}

// deleteLink is LinkBench delete_link: it removes the marker
// association (ltype 97) the setup creates. [Write]
func deleteLink() *workload.Query {
	return &workload.Query{
		ID:    "lb-delete-link",
		Class: engine.Write,
		Texts: map[engine.Dialect]string{
			engine.Cypher: `MATCH (:Obj {id: $src})-[l:LINK {ltype: $ltype}]->(:Obj {id: $dst})
DELETE l`,
			engine.ZuQL: `MATCH (:Obj {id: $src})-[l:LINK]->(:Obj {id: $dst})
WHERE l.ltype = $ltype
DELETE l`,
		},
		Params: workload.Fixed{P: workload.Params{
			"src": int64(0), "dst": int64(3), "ltype": int64(97),
		}},
		Setup: `MATCH (a:Obj {id: 0}), (b:Obj {id: 3})
CREATE (a)-[:LINK {ltype: 97, time: 0, payload: "seed"}]->(b)`,
		Setups: map[engine.Dialect]string{
			engine.ZuQL: `MATCH (a:Obj {id: 0}), (b:Obj {id: 3})
INSERT (a)-[:LINK {ltype: 97, time: 0, payload: 'seed'}]->(b)`,
		},
		PostCondition: `MATCH (:Obj {id: 0})-[l:LINK {ltype: 97}]->(:Obj {id: 3})
RETURN count(l) = 0`,
		PostConditions: map[engine.Dialect]string{
			engine.ZuQL: `MATCH (:Obj {id: 0})-[l:LINK]->(:Obj {id: 3})
WHERE l.ltype = 97
WITH count(l) AS c
RETURN c = 0 AS ok`,
		},
		Teardown: `MATCH (:Obj {id: 0})-[l:LINK {ltype: 97}]->(:Obj {id: 3})
DELETE l`,
		Teardowns: map[engine.Dialect]string{
			engine.ZuQL: `MATCH (:Obj {id: 0})-[l:LINK]->(:Obj {id: 3})
WHERE l.ltype = 97
DELETE l`,
		},
		AutocommitOK: true,
	}
}
