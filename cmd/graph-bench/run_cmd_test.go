package main

import (
	"strings"
	"testing"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

// full is the workload the focus tests narrow, three queries in a fixed
// order so the order check has something to say.
func full() *workload.Workload {
	return &workload.Workload{
		Name:   "micro-read",
		Family: "micro",
		Queries: []*workload.Query{
			{ID: "micro-point", Class: engine.PointRead},
			{ID: "micro-khop1", Class: engine.Traversal},
			{ID: "micro-varlen", Class: engine.Traversal},
		},
	}
}

func TestFocusKeepsTheNamedQueriesInWorkloadOrder(t *testing.T) {
	got, err := focus(full(), []string{"micro-varlen", "micro-point"})
	if err != nil {
		t.Fatalf("focus: %v", err)
	}
	if len(got.Queries) != 2 {
		t.Fatalf("kept %d queries, want 2", len(got.Queries))
	}
	if got.Queries[0].ID != "micro-point" || got.Queries[1].ID != "micro-varlen" {
		t.Errorf("kept %s and %s, want them in workload order", got.Queries[0].ID, got.Queries[1].ID)
	}
	if got.Name != "micro-read" || got.Family != "micro" {
		t.Errorf("focus changed the workload identity: %s/%s", got.Name, got.Family)
	}
}

func TestFocusLeavesTheOriginalAlone(t *testing.T) {
	wl := full()
	if _, err := focus(wl, []string{"micro-point"}); err != nil {
		t.Fatalf("focus: %v", err)
	}
	if len(wl.Queries) != 3 {
		t.Errorf("the source workload lost queries, %d left", len(wl.Queries))
	}
}

func TestFocusNamesTheQueryItCouldNotFind(t *testing.T) {
	_, err := focus(full(), []string{"micro-point", "micro-sp"})
	if err == nil {
		t.Fatal("focus accepted a query the workload does not have")
	}
	if !strings.Contains(err.Error(), "micro-sp") {
		t.Errorf("error does not name the missing query: %v", err)
	}
	if !strings.Contains(err.Error(), "micro-khop1") {
		t.Errorf("error does not list what the workload does have: %v", err)
	}
}

func TestFocusRefusesAMixedWorkload(t *testing.T) {
	wl := full()
	wl.Mix = &workload.Mix{Weights: map[string]float64{"micro-point": 1}}
	if _, err := focus(wl, []string{"micro-point"}); err == nil {
		t.Fatal("focus narrowed a mix, which is a different mix and not a smaller sample")
	}
}
