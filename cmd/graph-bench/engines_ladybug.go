//go:build ladybug

// Ladybug (Kuzu-lineage, in-process via cgo) registration. The adapter
// self-registers via init, so a blank import is the whole wiring. Built with
// -tags ladybug because it needs liblbug on the machine ($LBUG_INCLUDE /
// $LBUG_LIB, spec 09 §4).
package main

import _ "github.com/tamnd/graph-bench/engine/ladybug"
