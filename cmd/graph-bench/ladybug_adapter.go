//go:build ladybug

package main

import lbadapter "github.com/tamnd/graph-bench/adapter/ladybug"

func init() { registerTarget(lbadapter.New()) }
