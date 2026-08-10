package snb

import (
	"github.com/tamnd/graph-bench/workload"
)

// This file registers "snb-mix": the SNB Interactive v2 operation mix
// (spec 06 §3.4) over the queries the other snb workloads define — 72%
// short reads, 8% complex reads, 20% updates (19.8% inserts, 0.2% deletes
// per the v2 frequencies), each family's share spread evenly across its
// queries. Scheduling is deterministic (ADR-8); throughput is the headline
// metric. StreamKey is empty: the social dataset carries no pre-materialized
// dependency-ordered update stream (the deviation disclosed in 06 §3.5),
// the updates here are the stationary self-contained writes of snb-update.

func init() {
	workload.Register(&workload.Workload{
		Name:     "snb-mix",
		Title:    "SNB Interactive operation mix (72% short / 8% complex / 20% update)",
		Family:   "snb",
		Dataset:  "social-1k",
		Fidelity: "derived",
		Queries:  interactiveQueries(),
		Mix: &workload.Mix{
			Weights:   mixWeights(),
			StreamKey: "",
		},
	})
}

// interactiveQueries is the mix's query set: the interactive families,
// sharing the other workloads' query pointers (snb-bi is analytics-only and
// stays out of the mix).
func interactiveQueries() []*workload.Query {
	var qs []*workload.Query
	qs = append(qs, shortQueries...)
	qs = append(qs, complexQueries...)
	qs = append(qs, updateQueries...)
	return qs
}

// mixWeights spreads the v2 family frequencies evenly across each family's
// queries; the weights sum to 100.
func mixWeights() map[string]float64 {
	w := map[string]float64{}
	for _, q := range shortQueries {
		w[q.ID] = 72.0 / float64(len(shortQueries))
	}
	for _, q := range complexQueries {
		w[q.ID] = 8.0 / float64(len(complexQueries))
	}
	for _, q := range insertQueries {
		w[q.ID] = 19.8 / float64(len(insertQueries))
	}
	for _, q := range deleteQueries {
		w[q.ID] = 0.2 / float64(len(deleteQueries))
	}
	return w
}
