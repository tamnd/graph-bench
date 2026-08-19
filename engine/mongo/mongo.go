// Package mongo is the graph-bench adapter for MongoDB, over the wire
// through the official Go driver. It is the document engine in the
// comparison, and it is here for the same reason PostgreSQL is: a graph
// question does not stop being a graph question because the store is not
// a graph, and the interesting number is what the same question costs on
// each shape of store.
//
// # The data model
//
// Two collections. A node is {_id: <id>} and an edge is {src, dst}. The
// node id is the document _id, so its index comes free and is the one
// MongoDB itself keeps clustered in its catalog; the edge collection gets
// a compound index on (src, dst) and a second on (dst, src), which is the
// same out-adjacency and in-adjacency pair the relational adapters build,
// and the same second full copy of every edge.
//
// Nothing here is a graph store pretending: an adjacency read is an index
// range scan over the edge collection and a multi-hop expansion is a
// correlated $lookup or a $graphLookup, which is what MongoDB gives you
// and what a team choosing MongoDB for a connected dataset would write.
//
// # The dialect
//
// The Mongo dialect is a JSON object naming a collection, the columns the
// answer has in order, and an aggregation pipeline:
//
//	{"collection": "node",
//	 "columns": ["id"],
//	 "pipeline": [{"$match": {"$expr": {"$eq": ["$_id", {"$toLong": "$$id"}]}}},
//	              {"$project": {"_id": 0, "id": "$_id"}}]}
//
// The columns are declared rather than read off the first document
// because column order is part of an answer and BSON field order is not
// something a pipeline should have to be trusted about.
//
// Parameters are spelled $$name and bound through the aggregate's let, so
// a text is a constant and no value is ever interpolated into it. They
// arrive as strings, as they do for every engine here, and the pipeline
// converts with $toLong where it needs a number: the conversion is
// visible in the text rather than hidden in the adapter.
//
// # What is not used, and why
//
// $facet is avoided even where it would make a count-with-a-default
// trivial, because MongoDB documents that a $facet sub-pipeline never
// uses an index and always collection-scans. A count written that way
// would have measured a full scan and reported it as a point read. Where
// a query must return a row even when nothing matches, the pipeline
// starts from the node collection instead and reaches the edges through
// $lookup, which does use indexes.
//
// # No build tag
//
// The driver is pure Go, so this adapter is in every build. What it needs
// is a server, and without one it fails at Start saying so.
package mongo

import "github.com/tamnd/graph-bench/engine"

// Engine is the MongoDB descriptor.
type Engine struct{}

// New returns the descriptor.
func New() *Engine { return &Engine{} }

var _ engine.Engine = (*Engine)(nil)

// Info reports the engine's identity and what it can actually do.
//
// Transactions is false. MongoDB has multi-document transactions, but
// only on a replica set or a sharded cluster, and this adapter runs
// against a single mongod because that is the default deployment a
// container gives you. Declaring the capability and then failing at Begin
// would be worse than skipping the transactional workloads.
//
// VarLengthPaths and PathPredicates are true because $graphLookup is a
// bounded recursive expansion with a restrictSearchWithMatch filter.
// ShortestPaths is false: $graphLookup has a depthField, but it reports
// the depth at which its own traversal first reached a document, and it
// is documented as performing a breadth-first search only for the
// purposes of that field rather than guaranteeing a minimal path, so a
// number read out of it would not be a shortest path claim this suite
// should make.
func (e *Engine) Info() engine.Info {
	return engine.Info{
		Name:     "mongodb",
		Plane:    engine.Native,
		Dialects: []engine.Dialect{engine.Mongo},
		Caps: engine.Capabilities{
			Transactions:   false,
			BulkLoad:       true,
			Deletes:        true,
			VarLengthPaths: true,
			ShortestPaths:  false,
			PathPredicates: true,
			Algorithms:     nil,
			MaxConcurrency: 0,
			Persistent:     true,
		},
	}
}
