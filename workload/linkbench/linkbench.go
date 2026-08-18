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
// The three association reads carry a zu text as well, and it names the
// tables node and edge rather than Obj and LINK: zu is bulk-loaded by
// zu copy, which builds one node table and one edge table out of the
// edge list and keys the nodes by the id column, so those are the names
// the graph has. The pattern, the predicate on the association type and
// the projection are the same query. The three that read object
// properties have no zu text, because zu copy loads the rel file and
// nothing puts the node table's columns in the store yet.
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
			engine.ZuQL: `MATCH (:Obj {id: $src})-[l:LINK]->(b:Obj)
WHERE l.ltype = $ltype
RETURN b.id AS dst, l.time AS time, l.payload AS payload
ORDER BY time DESC, payload ASC
LIMIT 10000`,
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
		},
		Params: workload.Fixed{P: workload.Params{
			"id": int64(9000001), "otype": int64(1), "version": int64(0),
			"time": int64(0), "payload": "graph-bench",
		}},
		PostCondition: `MATCH (o:Obj {id: 9000001}) RETURN count(o) = 1`,
		Teardown:      `MATCH (o:Obj {id: 9000001}) DETACH DELETE o`,
		AutocommitOK:  true,
	}
}

// updateNode is LinkBench update_node: it bumps the version and swaps
// the payload of a scratch object the setup creates. [Write]
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
// operation itself failed. [Write]
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
		},
		Params: workload.Fixed{P: workload.Params{
			"src": int64(0), "dst": int64(1), "ltype": int64(99),
			"time": int64(0), "payload": "graph-bench",
		}},
		PostCondition: `MATCH (:Obj {id: 0})-[l:LINK {ltype: 99}]->(:Obj {id: 1})
RETURN count(l) = 1`,
		Teardown: `MATCH (:Obj {id: 0})-[l:LINK {ltype: 99}]->(:Obj {id: 1})
DELETE l`,
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
		},
		Params: workload.Fixed{P: workload.Params{
			"src": int64(0), "dst": int64(2), "ltype": int64(98), "payload": "updated",
		}},
		Setup: `MATCH (a:Obj {id: 0}), (b:Obj {id: 2})
CREATE (a)-[:LINK {ltype: 98, time: 0, payload: "seed"}]->(b)`,
		PostCondition: `MATCH (:Obj {id: 0})-[l:LINK {ltype: 98}]->(:Obj {id: 2})
RETURN l.payload = "updated"`,
		Teardown: `MATCH (:Obj {id: 0})-[l:LINK {ltype: 98}]->(:Obj {id: 2})
DELETE l`,
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
		},
		Params: workload.Fixed{P: workload.Params{
			"src": int64(0), "dst": int64(3), "ltype": int64(97),
		}},
		Setup: `MATCH (a:Obj {id: 0}), (b:Obj {id: 3})
CREATE (a)-[:LINK {ltype: 97, time: 0, payload: "seed"}]->(b)`,
		PostCondition: `MATCH (:Obj {id: 0})-[l:LINK {ltype: 97}]->(:Obj {id: 3})
RETURN count(l) = 0`,
		Teardown: `MATCH (:Obj {id: 0})-[l:LINK {ltype: 97}]->(:Obj {id: 3})
DELETE l`,
		AutocommitOK: true,
	}
}
