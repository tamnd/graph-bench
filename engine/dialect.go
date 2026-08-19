package engine

// Dialect names a query language variant a workload text is written in.
// A workload query carries Texts keyed by dialect; an engine carries a
// preference-ordered chain. Resolution is uniform for every engine: the
// first dialect in the chain with a text wins, and no text in any chain
// means SKIP — never a silent fallback (ADR-4).
type Dialect string

const (
	// Cypher is the openCypher / Cypher 5 common core every Cypher engine
	// accepts.
	Cypher Dialect = "cypher"
	// Cypher25 is Neo4j's Cypher 25 where it diverges from the core.
	Cypher25 Dialect = "cypher25"
	// KuzuCy is the Kùzu/Ladybug dialect (typed casts, SHORTEST syntax).
	KuzuCy Dialect = "kuzu"
	// MAGE is Memgraph's procedure surface: `CALL pagerank.get()` and the
	// other MAGE modules, plus Memgraph's *BFS / *WSHORTEST expansions.
	//
	// It is its own dialect rather than Cypher because those procedures are
	// one vendor's library, not part of any Cypher any other engine accepts.
	// Filed under Cypher they resolved on every Cypher engine and failed to
	// parse there — and a parse failure is a FAIL, which discards the whole
	// workload's measurements for that engine. As a dialect they simply do
	// not resolve, so an engine without MAGE records "no-dialect-text",
	// which is what is actually true about it.
	MAGE Dialect = "mage"
	// ZuQL is zu's read-only GQL-flavored dialect.
	ZuQL Dialect = "zuql"
	// Prim is the primitive dialect: one line naming an operation and its
	// parameters, for a storage engine that has no query language at all.
	// `khop out 2 seed=$seed as n` is a text in it.
	//
	// It is a dialect and not a special case inside one adapter because of
	// the same rule the rest of this file is about. An engine with no
	// parser still has to be told which question to answer, and the choice
	// belongs next to the Cypher and zuQL spellings of that question where
	// a reader can see all three together. An adapter that instead matched
	// on QueryID would be answering a question nobody wrote down, and a
	// query with no Prim text would quietly become whatever the adapter
	// guessed rather than a SKIP.
	Prim Dialect = "prim"
	// SQL is the portable SQL a relational engine answers a graph question
	// in: the node table, the edge table, a join per hop and a recursive
	// CTE where the hop count is not fixed. It is written to the common
	// core SQLite, DuckDB and PostgreSQL all accept, so one text serves
	// all three and the difference between their numbers is the engine
	// rather than three hand-written queries.
	//
	// Two conventions belong to this dialect and to no other. A parameter
	// is spelled $name, as it is in Cypher, and each adapter rewrites it
	// to whatever its driver wants (? or $1); the text never carries a
	// driver's placeholder syntax. And a result column may be aliased
	// "name::type" to say what the value is, because SQLite has no boolean
	// type and would otherwise answer an existence probe with the integer
	// 1 where the reference says true. The adapter names the column `name`
	// and decodes the value as `type`. Only bool is annotated today, and
	// on an engine that already returns a boolean it changes nothing.
	SQL Dialect = "sql"
	// SQLPGQ is SQL:2023 property-graph query syntax (DuckPGQ seam).
	SQLPGQ Dialect = "sqlpgq"
	// Mongo is a MongoDB aggregation pipeline as JSON: an array of stages,
	// with $graphLookup where the question is a traversal. Parameters are
	// spelled $$name, which is how a pipeline names a variable, and the
	// adapter binds them through the aggregation's let.
	Mongo Dialect = "mongo"
)

// ResolveText picks the text for an engine's dialect chain. It returns
// the dialect used and its text, or ok == false when no dialect in the
// chain has a text (the caller records a SKIP with reason
// "no-dialect-text").
func ResolveText(chain []Dialect, texts map[Dialect]string) (Dialect, string, bool) {
	for _, d := range chain {
		if t, ok := texts[d]; ok && t != "" {
			return d, t, true
		}
	}
	return "", "", false
}
