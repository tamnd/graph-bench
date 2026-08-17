// Engine registration for every build: zu registers here, and whether it
// can actually start depends on the zuinproc tag, since the adapter links
// libzu. Registration is a cheap descriptor either way, and a binary built
// without the tag fails at Start with the build line to use rather than
// reporting an unknown engine. Engine construction reads its environment
// ($ZU_BIN and the rest of the discovery order, spec 09 §4) at Start.
//
// Bolt-plane adapters (neo4j, memgraph) register in engines_bolt.go under
// -tags bolt; the Ladybug in-process adapter self-registers via its own init
// and is blank-imported in engines_ladybug.go under -tags ladybug.
package main

import (
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/engine/zu"
)

func init() {
	engine.Register(zu.New())
}
