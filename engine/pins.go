package engine

// Pin records the version of an engine the suite is validated against.
// This table is the single source of truth (spec 04 §1): Docker files,
// CI, and docs must match it, and `graph-bench doctor` verifies they do.
// Live-reported versions (Session.Version) are what results stamp; a
// mismatch between pin and live version is surfaced by doctor, never
// silently accepted.
type Pin struct {
	Engine string
	Pinned string
	Source string // where the pin applies: "docker", "brew", "go.mod", "local"
}

// Pins is the v0.3.0 pin table.
var Pins = []Pin{
	{Engine: "zu", Pinned: "../zu local build (ZU_BIN)", Source: "local"},
	{Engine: "neo4j", Pinned: "neo4j:2026.06.0", Source: "docker"},
	{Engine: "neo4j-go-driver", Pinned: "v6.2.0", Source: "go.mod"},
	{Engine: "ladybug", Pinned: "0.19.1", Source: "brew"},
	{Engine: "memgraph", Pinned: "memgraph/memgraph-mage:3.10.0", Source: "docker"},
}

// PinFor returns the pin for an engine name, if recorded.
func PinFor(name string) (Pin, bool) {
	for _, p := range Pins {
		if p.Engine == name {
			return p, true
		}
	}
	return Pin{}, false
}
