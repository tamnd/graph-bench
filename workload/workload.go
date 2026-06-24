// Package workload is the query catalog: the logical queries the suite asks of
// every engine, each carrying a class, a per-dialect set of texts, a curated
// parameter source, and a reference-answer strategy. A workload defines a
// question once; the harness asks it of every engine in that engine's own
// language and validates that every engine answered the same question.
//
// This package holds the model (Workload, WorkloadQuery, Dialect, ParamSource,
// RefStrategy, CompareSpec) and the registry the CLI lists and the gate selects
// from. The query families live in subpackages (workload/micro, workload/snb,
// workload/lsqb), each registering its workloads in an init. See the spec at
// notes/Spec/2060/bench/05-workloads.md, which this package realizes.
package workload

import (
	"sort"

	"github.com/tamnd/graph-bench/target"
)

// Workload is a named set of queries with a reference dataset and a run mix. It
// is the unit the CLI lists ("graph-bench run --workload micro") and the unit a
// gate selects. A family registers its workloads at init (see Register).
type Workload struct {
	// Name is the stable identifier, e.g. "micro", "snb-short", "lsqb". Unique
	// across the registry.
	Name string

	// Title is a one-line human description for the CLI listing.
	Title string

	// Dataset names the dataset this workload runs against, resolved by the
	// dataset package (e.g. "rmat-s18", "grid-1000x1000", "snb-sf1"). See the
	// dataset spec, doc 04.
	Dataset string

	// Queries is the ordered catalog. Each entry is an abstract query with a
	// text per dialect and a parameter source.
	Queries []*WorkloadQuery

	// Mix, if non-empty, gives the relative firing weight of each query id in a
	// mixed run. An empty Mix means every query runs in isolation only, at equal
	// weight, which is the default for the micro and lsqb families.
	Mix map[string]float64
}

// Classes returns the distinct budget classes this workload's queries exercise,
// in enum order, so the gate knows which ceilings apply. It is derived from the
// queries rather than stored, so it cannot drift from the catalog.
func (w *Workload) Classes() []target.Class {
	var seen [5]bool
	for _, q := range w.Queries {
		if int(q.Class) >= 0 && int(q.Class) < len(seen) {
			seen[q.Class] = true
		}
	}
	var out []target.Class
	for c := range seen {
		if seen[c] {
			out = append(out, target.Class(c))
		}
	}
	return out
}

// Query returns the catalog query with the given id, or false. It is the lookup
// the gate and the CLI use to select a single query by id.
func (w *Workload) Query(id string) (*WorkloadQuery, bool) {
	for _, q := range w.Queries {
		if q.ID == id {
			return q, true
		}
	}
	return nil, false
}

// WorkloadQuery is one logical query: an id, a class, the per-engine texts, a
// parameter source, and a reference-answer strategy. It is abstract until the
// harness resolves it into a concrete target.Query (see Resolve) for one engine
// just before the query reaches that engine's Driver.
type WorkloadQuery struct {
	// ID is the stable id, e.g. "micro-khop2", "snb-is2", "lsqb-q5".
	ID string

	// Class is the budget class the query is measured against.
	Class target.Class

	// Texts holds the query text keyed by dialect. The Cypher entry is shared by
	// every Bolt engine and gr in-process; the native entries carry the
	// equivalent text for engines that do not speak Cypher. The Cypher entry is
	// mandatory; a missing native text is a blank matrix cell, not a failure.
	Texts map[Dialect]string

	// PoolKey, when non-empty, names the key in the dataset's params.json that
	// holds this query's curated parameter pool. The harness loads the pool at
	// run time, auto-curating if params.json is absent. Queries with a PoolKey
	// and a runtime-loaded pool use that pool; Params is the static fallback.
	PoolKey string

	// Params is the parameter source: either a fixed set or a draw from the
	// dataset's curated pool. See ParamSource.
	Params ParamSource

	// Reference is the reference-answer strategy: how the engine-independent
	// expected answer is computed once per dataset and how an engine's answer is
	// compared to it. See reference.go.
	Reference RefStrategy
}

// Resolve binds an abstract query to one engine: it picks the dialect's text,
// attaches the bound parameters and the precomputed reference answer, and
// carries the id and class through unchanged. The result is the target.Query the
// Driver runs. ok is false when the query carries no text for the dialect, which
// is the blank-cell case (the engine does not speak this query's language); the
// caller records a blank, not a failure.
func (q *WorkloadQuery) Resolve(d Dialect, ref *target.Answer) (target.Query, target.Params, bool) {
	text, ok := q.Texts[d]
	if !ok {
		return target.Query{}, nil, false
	}
	var params target.Params
	if q.Params != nil {
		params = q.Params.Next()
	}
	return target.Query{
		ID:        q.ID,
		Class:     q.Class,
		Text:      text,
		Reference: ref,
	}, params, true
}

// Dialect identifies a plane-plus-language pair for keying query texts.
type Dialect int

const (
	// Cypher is the text for the Bolt plane and gr in-process: openCypher. It is
	// the primary dialect and the one every v1 engine but the deferred native
	// ones speaks.
	Cypher Dialect = iota
	// SQLPGQ is the text for DuckPGQ: SQL/PGQ, the property-graph query syntax.
	SQLPGQ
	// AGE is the text for Apache AGE: an openCypher subset wrapped in cypher(...)
	// over the Postgres wire.
	AGE
	// SQL is the text for SQLite: a recursive common table expression, the
	// naive-relational floor.
	SQL
	// KuzuCypher is the text for Kuzu-family engines (LadybugDB, Kuzu): openCypher
	// with Kuzu extensions. Kuzu does not implement shortestPath(); it uses a
	// variable-length path syntax with the SHORTEST keyword instead.
	KuzuCypher
)

// String returns the dialect's stable name for stamps and reports.
func (d Dialect) String() string {
	switch d {
	case Cypher:
		return "cypher"
	case SQLPGQ:
		return "sqlpgq"
	case AGE:
		return "age"
	case SQL:
		return "sql"
	case KuzuCypher:
		return "kuzu-cypher"
	default:
		return "unknown"
	}
}

// registry holds the registered workloads by name.
var registry = map[string]*Workload{}

// Register adds a workload to the global catalog. It is called from init in each
// family package. It panics on a duplicate name, which is a programming error
// caught at startup, not a runtime condition.
func Register(w *Workload) {
	if w.Name == "" {
		panic("workload: cannot register a workload with an empty name")
	}
	if _, dup := registry[w.Name]; dup {
		panic("workload: duplicate registration: " + w.Name)
	}
	registry[w.Name] = w
}

// Lookup returns a workload by name, or false. Used by the CLI's --workload flag
// and the gate's subset selection.
func Lookup(name string) (*Workload, bool) {
	w, ok := registry[name]
	return w, ok
}

// All returns every registered workload, sorted by name, for the CLI listing.
func All() []*Workload {
	out := make([]*Workload, 0, len(registry))
	for _, w := range registry {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Bounded returns a synthetic Workload containing only the queries from the
// registered "micro-grid" workload that exercise bounded classes (PointRead,
// Traversal, Subgraph, Write). It is what TestSmokeGate selects: the bounded
// read queries on a synthetic grid dataset, fast and low-variance, with no SNB
// dataset required. It returns nil when micro-grid is not in the registry (the
// caller must import _ "github.com/tamnd/graph-bench/workload/micro" to ensure
// registration).
func Bounded() *Workload {
	w, ok := registry["micro-grid"]
	if !ok {
		return nil
	}
	// Filter to bounded queries only (exclude any Write-class queries that happen
	// to be included in the workload). For micro-grid all queries are Traversal,
	// but this is defensive.
	var qs []*WorkloadQuery
	for _, q := range w.Queries {
		switch q.Class {
		case target.PointRead, target.Traversal, target.Subgraph, target.Write:
			qs = append(qs, q)
		}
	}
	return &Workload{
		Name:    "bounded",
		Title:   "Bounded smoke-gate subset (micro-grid read queries)",
		Dataset: w.Dataset,
		Queries: qs,
	}
}
