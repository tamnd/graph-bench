// Package workload defines the workload model — named sets of queries
// with parameters, mixes, and engine-independent references — and the
// registry the CLI serves. The governing rule (spec 03 §6): a workload
// defines a logical question once; the harness asks it of every engine
// in that engine's dialect and validates every engine against one
// reference. A read query that cannot state its reference does not ship.
package workload

import (
	"fmt"
	"sort"
	"sync"

	"github.com/tamnd/graph-bench/engine"
)

// Workload is a named set of queries over one dataset.
type Workload struct {
	// Name is the registry key: "micro-read", "snb-short", "linkbench".
	Name string

	// Title is the one-line human description.
	Title string

	// Family groups related workloads: "micro", "snb", "finbench".
	Family string

	// Dataset names the dataset this workload runs on, resolved by the
	// dataset layer (a generator recipe name or a pin name). Empty means
	// statements-only: the workload seeds its own data.
	Dataset string

	// Fidelity is the coverage-map disclosure rendered in report footers
	// (spec 07 §6): "spec-following", "derived", "faithful",
	// "harness-native".
	Fidelity string

	// Queries in registration order.
	Queries []*Query

	// Mix, when non-nil, defines the interleaved operation mix.
	Mix *Mix

	// Analytics marks workloads measured under the analytics protocol
	// (single-stream repetitions, not concurrency).
	Analytics bool

	// ValidationScale, when set, is the largest scale label at which full
	// validation runs; larger runs validate on sampled parameters.
	ValidationScale string
}

// Query is one logical question.
type Query struct {
	ID    string
	Class engine.Class

	// Texts per dialect. Resolution against an engine's chain is uniform
	// (engine.ResolveText); a blank chain means SKIP.
	Texts map[engine.Dialect]string

	// PoolKey names the curated parameter pool; empty means Params
	// supplies fixed or generated parameters.
	PoolKey string

	// Params yields deterministic parameter draws; every engine sees the
	// identical sequence.
	Params ParamSource

	// Reference computes the expected answer and how to compare it.
	// Required for read classes; write queries use PostCondition instead.
	Reference *RefStrategy

	// Algorithm, when set, names the native kernel this query needs
	// ("pagerank"); the runner SKIPs engines that do not declare it.
	Algorithm string

	// NeedsPathPredicate marks a query that constrains a variable-length
	// pattern with a run-time predicate over the relationships it traverses;
	// the runner SKIPs engines that do not declare Caps.PathPredicates.
	NeedsPathPredicate bool

	// NeedsShortestPath marks a query that asks for a shortest path rather
	// than a bounded expansion; the runner SKIPs engines that do not declare
	// Caps.ShortestPaths.
	//
	// Without the flag the dialect chain decides, and it decides wrong. An
	// engine with no shortest-path syntax has no text of its own, so
	// resolution falls through to the plain Cypher text and hands it a
	// shortestPath() call it cannot parse. That is a FAIL, and one FAIL
	// discards measurement for every query in the workload, so an engine that
	// should have reported "I don't do shortest paths" reports nothing at all.
	NeedsShortestPath bool

	// Setup and Teardown are statements run around write queries so
	// repetitions are stationary.
	Setup, Teardown string

	// PostCondition validates write effects: a query text (Cypher core)
	// whose result is compared against Expected.
	PostCondition string

	// PostConditions spells the check for dialects whose text differs from
	// the Cypher core, resolved against the dialect the query itself
	// resolved to and falling back to PostCondition.
	//
	// It exists for the same reason Texts is a map. An engine whose labels
	// are spelled differently can run the write and still be unable to read
	// the check, and a check that will not parse fails the query, which
	// discards the measurement for every query in the workload. One
	// unresolvable check would cost the whole write comparison.
	PostConditions map[engine.Dialect]string

	// AutocommitOK marks write queries that may run outside an explicit
	// transaction on engines without transaction support.
	AutocommitOK bool
}

// Check returns the post-condition text for the dialect the query
// resolved to, which is the dialect-specific one when there is one and
// the Cypher core otherwise. An empty string means the write has no
// post-condition on this engine.
func (q *Query) Check(d engine.Dialect) string {
	if t, ok := q.PostConditions[d]; ok && t != "" {
		return t
	}
	return q.PostCondition
}

// Mix is a weighted operation mix with deterministic scheduling.
type Mix struct {
	// Weights map query IDs to relative frequencies (normalized at
	// schedule build).
	Weights map[string]float64

	// StreamKey, when set, names the pre-materialized dependency-ordered
	// update stream in the dataset (SNB v2 inserts/deletes).
	StreamKey string
}

// Classes returns the distinct classes present, in canonical order.
func (w *Workload) Classes() []engine.Class {
	present := map[engine.Class]bool{}
	for _, q := range w.Queries {
		present[q.Class] = true
	}
	var out []engine.Class
	for _, c := range engine.Classes() {
		if present[c] {
			out = append(out, c)
		}
	}
	return out
}

// Query returns the query with the given ID.
func (w *Workload) Query(id string) (*Query, bool) {
	for _, q := range w.Queries {
		if q.ID == id {
			return q, true
		}
	}
	return nil, false
}

var (
	regMu    sync.Mutex
	registry = map[string]*Workload{}
)

// Register adds a workload. It panics on duplicates and on read queries
// without references — an unverifiable question is a spec violation
// caught at init, not at run time.
func Register(w *Workload) {
	regMu.Lock()
	defer regMu.Unlock()
	if w.Name == "" {
		panic("workload: Register with empty name")
	}
	if _, dup := registry[w.Name]; dup {
		panic(fmt.Sprintf("workload: duplicate registration %q", w.Name))
	}
	for _, q := range w.Queries {
		if q.Class != engine.Write && q.Reference == nil {
			panic(fmt.Sprintf("workload %s: read query %s has no reference (spec 03 §6)", w.Name, q.ID))
		}
		if q.Class == engine.Write && q.PostCondition == "" && len(q.PostConditions) == 0 && q.Reference == nil {
			panic(fmt.Sprintf("workload %s: write query %s has neither post-condition nor reference", w.Name, q.ID))
		}
	}
	registry[w.Name] = w
}

// Lookup returns a registered workload.
func Lookup(name string) (*Workload, error) {
	regMu.Lock()
	defer regMu.Unlock()
	w, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("workload %q is not registered (graph-bench list workloads)", name)
	}
	return w, nil
}

// All returns registered workloads sorted by name.
func All() []*Workload {
	regMu.Lock()
	defer regMu.Unlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]*Workload, 0, len(names))
	for _, n := range names {
		out = append(out, registry[n])
	}
	return out
}
