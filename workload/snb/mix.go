package snb

import (
	"github.com/tamnd/graph-bench/workload"
)

func init() {
	workload.Register(mixWorkload)
}

// mixWorkload is the "snb-mix" workload: all SNB Interactive queries fired at
// the LDBC v2 operation mix proportions in a single concurrent run.
// The mix is approximately 72% short reads, 8% complex reads, 20% inserts,
// 0.2% deletes, per the LDBC SNB Interactive v2 reference driver frequencies.
//
// The Mix weights encode the relative firing rates. Within short reads,
// IS1-IS5 fire at weight 12 and IS6-IS7 fire at weight 6 (the LDBC frequency
// table where IS1-IS5 have frequency 2 and IS6-IS7 have frequency 1). Complex
// reads and writes are weighted uniformly within their group.
//
// The snb-mix run produces the realistic throughput number: queries-per-second
// under the representative blend with reads and writes contending for the engine
// concurrently (F6). The per-class latency under the mix, compared to the
// isolation latency from snb-short and snb-complex, reveals write interference.
var mixWorkload = &workload.Workload{
	Name:    "snb-mix",
	Title:   "SNB Interactive v2 realistic mix (72% short / 8% complex / 20% insert / 0.2% delete)",
	Dataset: "snb-sf1",

	// Queries for snb-mix are the union of snb-short + snb-complex + snb-write.
	// The Mix map below controls firing proportions; Queries lists them for the
	// registry to enumerate. The runner pulls queries by id from the catalog.
	Queries: mixQueries(),

	// Mix maps query id -> relative firing weight. Weights do not need to sum to
	// any specific value; the runner normalises to a probability distribution.
	Mix: map[string]float64{
		// Short reads: IS1-IS5 weight 12, IS6-IS7 weight 6 (LDBC freq table 2:1).
		// Total: 5*12 + 2*6 = 72.
		"snb-is1": 12.0,
		"snb-is2": 12.0,
		"snb-is3": 12.0,
		"snb-is4": 12.0,
		"snb-is5": 12.0,
		"snb-is6": 6.0,
		"snb-is7": 6.0,

		// Complex reads: uniform at 8/6 ≈ 1.333 each. Total: 8.
		"snb-ic1":  8.0 / 6.0,
		"snb-ic2":  8.0 / 6.0,
		"snb-ic6":  8.0 / 6.0,
		"snb-ic8":  8.0 / 6.0,
		"snb-ic9":  8.0 / 6.0,
		"snb-ic11": 8.0 / 6.0,

		// Inserts: uniform at 20/8 = 2.5 each. Total: 20.
		"snb-iu1": 2.5,
		"snb-iu2": 2.5,
		"snb-iu3": 2.5,
		"snb-iu4": 2.5,
		"snb-iu5": 2.5,
		"snb-iu6": 2.5,
		"snb-iu7": 2.5,
		"snb-iu8": 2.5,

		// Deletes: uniform at 0.2/8 = 0.025 each. Total: 0.2.
		"snb-id1": 0.025,
		"snb-id2": 0.025,
		"snb-id3": 0.025,
		"snb-id4": 0.025,
		"snb-id5": 0.025,
		"snb-id6": 0.025,
		"snb-id7": 0.025,
		"snb-id8": 0.025,
	},
}

// mixQueries assembles the query list for snb-mix by collecting from the three
// component workloads. This keeps snb-mix as a thin Mix-only overlay so the
// query definitions live exactly once (in snb.go, complex.go, writes.go).
func mixQueries() []*workload.WorkloadQuery {
	var all []*workload.WorkloadQuery
	for _, wl := range []*workload.Workload{shortWorkload, complexWorkload, writeWorkload} {
		all = append(all, wl.Queries...)
	}
	return all
}
