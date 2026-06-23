//go:build bolt

// This file registers the Bolt-plane adapters when the binary is built with
// -tags bolt. Each adapter reads its URI and credentials from environment
// variables at startup with sensible localhost defaults so a plain
// "graph-bench run --engines neo4j" works against a local instance without
// any extra flags.
//
// Environment variables (all optional):
//
//	GR_BOLT_URI   (default bolt://127.0.0.1:7689)
//	NEO4J_URI     (default bolt://127.0.0.1:7687)
//	NEO4J_USER    (default "neo4j")
//	NEO4J_PASS    (default "none")
//	MEMGRAPH_URI  (default bolt://127.0.0.1:7687)
//	MEMGRAPH_USER (default "")
//	MEMGRAPH_PASS (default "")
package main

import (
	"os"

	grAdapter "github.com/tamnd/graph-bench/adapter/gr"
	"github.com/tamnd/graph-bench/adapter/memgraph"
	"github.com/tamnd/graph-bench/adapter/neo4j"
)

func init() {
	grBoltURI := envOr("GR_BOLT_URI", "bolt://127.0.0.1:7689")
	registerTarget(grAdapter.NewBolt(grBoltURI))

	neoURI := envOr("NEO4J_URI", "bolt://127.0.0.1:7687")
	neoUser := envOr("NEO4J_USER", "neo4j")
	neoPass := envOr("NEO4J_PASS", "none")
	registerTarget(neo4j.New(neoURI, neoUser, neoPass))

	mgURI := envOr("MEMGRAPH_URI", "bolt://127.0.0.1:7687")
	mgUser := envOr("MEMGRAPH_USER", "")
	mgPass := envOr("MEMGRAPH_PASS", "")
	registerTarget(memgraph.New(mgURI, mgUser, mgPass))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
