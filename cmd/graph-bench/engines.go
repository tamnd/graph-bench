// Engine registration for the default build: the zu adapter runs on the
// Subprocess plane, is pure Go, and needs no build tag. Engine construction
// reads its environment ($ZU_BIN and the rest of the discovery order,
// spec 09 §4) at Start, not here — registration is a cheap descriptor.
//
// Bolt-plane adapters (neo4j, memgraph) register in engines_bolt.go under
// -tags bolt; the Ladybug in-process adapter self-registers via its own init
// and is blank-imported in engines_ladybug.go under -tags ladybug. The same
// zu engine reached in-process through libzu registers as "zu-capi" in
// engines_zuinproc.go under -tags zuinproc.
package main

import (
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/engine/zu"
)

func init() {
	engine.Register(zu.New())
}
