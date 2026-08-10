package snb

import (
	"fmt"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

// Parameter pool keys (spec 05 §4). workload/curate.go does not exist yet,
// so this file defines the pools inline: a small deterministic curation from
// the dataset CSVs under the same key names a future curate step would use.
// Bind prefers a pool shipped with the dataset (params.json via
// Dataset.Params) and falls back to the inline curation, so a dataset that
// grows curated pools later wins without a code change.
const (
	// PoolPersonID: {personId}.
	PoolPersonID = "person-id"
	// PoolPostID: {postId}.
	PoolPostID = "post-id"
	// PoolForumID: {forumId}.
	PoolForumID = "forum-id"
	// PoolPersonPair: {person1Id, person2Id}, curated to KNOWS-connected
	// pairs at distance >= 2 where possible so ic13 finds a path.
	PoolPersonPair = "person-pair"
	// PoolPersonName: {personId, firstName}, the firstName of a person
	// within 3 KNOWS hops of personId so ic1 is non-degenerate.
	PoolPersonName = "person-name"
	// PoolPersonDate: {personId, maxDate, minDate}, window bounds inside
	// the generator's five-year creationDate window.
	PoolPersonDate = "person-date"
)

// Pool sizes and the date thresholds (seconds; the generator draws
// creationDate uniformly over five years of seconds).
const (
	personPoolSize = 32
	postPoolSize   = 32
	forumPoolSize  = 16
	pairPoolSize   = 16
	yearSeconds    = int64(365 * 86400)
	poolMaxDate    = 4 * yearSeconds // ~80% of posts fall before this
	poolMinDate    = 1 * yearSeconds // ~80% of likes fall after this
)

// Pools curates every parameter pool this package's queries name, keyed by
// pool key. Deterministic: stride sampling over the dataset's stable row
// order plus breadth-first walks with sorted adjacency.
func Pools(ds engine.Dataset) (map[string][]workload.Params, error) {
	m, err := loadSocial(ds)
	if err != nil {
		return nil, err
	}
	pools := map[string][]workload.Params{}

	// person-id, person-date: stride over persons.
	for _, i := range stride(len(m.persons), personPoolSize) {
		id := workload.IDValue(m.persons[i].id)
		pools[PoolPersonID] = append(pools[PoolPersonID], workload.Params{"personId": id})
		pools[PoolPersonDate] = append(pools[PoolPersonDate], workload.Params{
			"personId": id, "maxDate": poolMaxDate, "minDate": poolMinDate,
		})
	}

	// post-id: stride over posts.
	for _, i := range stride(len(m.posts), postPoolSize) {
		pools[PoolPostID] = append(pools[PoolPostID], workload.Params{"postId": workload.IDValue(m.posts[i].id)})
	}

	// forum-id: stride over forums.
	for _, i := range stride(len(m.forums), forumPoolSize) {
		pools[PoolForumID] = append(pools[PoolForumID], workload.Params{"forumId": workload.IDValue(m.forums[i].id)})
	}

	// person-pair and person-name: breadth-first from stride seeds.
	for _, i := range stride(len(m.persons), pairPoolSize) {
		dist := m.bfs(i, 3)
		if j := lowestAt(dist, 2); j >= 0 {
			pools[PoolPersonPair] = append(pools[PoolPersonPair], workload.Params{
				"person1Id": workload.IDValue(m.persons[i].id), "person2Id": workload.IDValue(m.persons[j].id),
			})
		} else if j := lowestAt(dist, 1); j >= 0 {
			pools[PoolPersonPair] = append(pools[PoolPersonPair], workload.Params{
				"person1Id": workload.IDValue(m.persons[i].id), "person2Id": workload.IDValue(m.persons[j].id),
			})
		}
		if j := lowestAt(dist, 1); j >= 0 {
			pools[PoolPersonName] = append(pools[PoolPersonName], workload.Params{
				"personId": workload.IDValue(m.persons[i].id), "firstName": m.persons[j].firstName,
			})
		}
	}

	for _, key := range []string{PoolPersonID, PoolPostID, PoolForumID, PoolPersonPair, PoolPersonName, PoolPersonDate} {
		if len(pools[key]) == 0 {
			return nil, fmt.Errorf("snb: curated pool %q is empty for dataset %q", key, ds.Name())
		}
	}
	return pools, nil
}

// Bind resolves every pooled query's parameters against the dataset: a
// dataset-shipped pool (params.json) wins, the inline curation from Pools is
// the fallback. The harness calls Bind after the dataset is resolved and
// before verification; an unbound pooled query yields a draw of nil, which
// the references reject with a "pools not bound" error.
func Bind(ds engine.Dataset) error {
	var curated map[string][]workload.Params
	for _, q := range allQueries() {
		if q.PoolKey == "" {
			continue
		}
		pool, err := ds.Params(q.PoolKey)
		if err != nil {
			return fmt.Errorf("snb: dataset pool %q: %w", q.PoolKey, err)
		}
		if len(pool) == 0 {
			if curated == nil {
				curated, err = Pools(ds)
				if err != nil {
					return err
				}
			}
			pool = curated[q.PoolKey]
		}
		if len(pool) == 0 {
			return fmt.Errorf("snb: no pool for key %q (query %s)", q.PoolKey, q.ID)
		}
		q.Params = workload.NewPoolSource(pool)
	}
	return nil
}

// allQueries returns every query this package registers, across all five
// workloads (the mix shares the other workloads' query pointers, so binding
// once binds everywhere).
func allQueries() []*workload.Query {
	var qs []*workload.Query
	qs = append(qs, shortQueries...)
	qs = append(qs, complexQueries...)
	qs = append(qs, updateQueries...)
	qs = append(qs, biQueries...)
	return qs
}

// stride returns up to k evenly spaced indices over [0, n), always including
// index 0 when n > 0.
func stride(n, k int) []int {
	if n <= 0 {
		return nil
	}
	if k > n {
		k = n
	}
	out := make([]int, 0, k)
	for i := 0; i < k; i++ {
		out = append(out, i*n/k)
	}
	return out
}

// lowestAt returns the lowest index whose distance equals d, or -1.
func lowestAt(dist []int, d int) int {
	for i, v := range dist {
		if v == d {
			return i
		}
	}
	return -1
}
