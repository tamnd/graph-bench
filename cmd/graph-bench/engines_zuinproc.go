//go:build zuinproc

// zu in-process registration (libzu over cgo). Built with -tags zuinproc
// because it needs libzu on the machine ($ZU_INCLUDE / $ZU_LIB, or a
// sibling zu checkout built with cargo build --release -p zu-capi). It
// registers as "zu-capi" and sits alongside "zu", so one run measures
// the same engine on both planes and the subprocess frame cost is the
// difference between the two columns.
package main

import (
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/engine/zu"
)

func init() {
	engine.Register(zu.NewInproc())
}
