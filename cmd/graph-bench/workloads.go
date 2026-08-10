// Workload family registration. Each family package registers its workloads
// via init, so importing it is the whole wiring. The list below is the full
// v0.3 catalog (spec 06–07).
package main

import (
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
	"github.com/tamnd/graph-bench/workload/finbench"
	_ "github.com/tamnd/graph-bench/workload/g500"
	_ "github.com/tamnd/graph-bench/workload/galytics"
	_ "github.com/tamnd/graph-bench/workload/gap"
	"github.com/tamnd/graph-bench/workload/linkbench"
	_ "github.com/tamnd/graph-bench/workload/lsqb"
	_ "github.com/tamnd/graph-bench/workload/micro"
	"github.com/tamnd/graph-bench/workload/snb"
)

// familyBinders maps a workload family to the pool binder its package
// exports. These families curate their own parameters — SNB needs person
// pairs that are actually connected, FinBench needs time windows that
// actually contain transfers, LinkBench needs the power-law hot spots —
// and a generic id sampler cannot produce any of that. Without this wiring
// their pooled queries would fall through to the name-shaped fallback in
// curate_cmd.go and measure the wrong thing while still reporting a number.
var familyBinders = map[string]func(*workload.Workload, engine.Dataset) error{
	"snb":       func(_ *workload.Workload, ds engine.Dataset) error { return snb.Bind(ds) },
	"finbench":  finbench.Bind,
	"linkbench": linkbench.Bind,
}

// bindFamilyPools runs the family's own binder when it has one, reporting
// whether it bound anything. A family without a binder draws through the
// generic path.
func bindFamilyPools(wl *workload.Workload, ds engine.Dataset) (bool, error) {
	bind, ok := familyBinders[wl.Family]
	if !ok {
		return false, nil
	}
	if err := bind(wl, ds); err != nil {
		return false, err
	}
	return true, nil
}
