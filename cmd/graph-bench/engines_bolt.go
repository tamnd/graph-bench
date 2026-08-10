//go:build bolt

// Bolt-plane engine registration. Built with -tags bolt because the Bolt
// driver dependency stays out of the default binary. The adapters read their
// connection environment at Start (spec 09 §4):
//
//	NEO4J_URI    (default bolt://127.0.0.1:7687)
//	NEO4J_USER   (default "neo4j")
//	NEO4J_PASS   (default empty)
//	MEMGRAPH_URI (default bolt://127.0.0.1:7687)
//
// The run verb may override the URI per run when it manages the server
// container itself (setup.Start), by setting Config["uri"].
package main

import (
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/engine/memgraph"
	"github.com/tamnd/graph-bench/engine/neo4j"
)

func init() {
	engine.Register(neo4j.New())
	engine.Register(memgraph.New())
}
