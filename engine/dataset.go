package engine

// Dataset is what Session.Load consumes. dataset.Set is the canonical
// implementation; StatementsDataset covers statement-seeded workloads.
type Dataset interface {
	// Name is the dataset's stable name ("snb-sf1", "grid-100x100").
	Name() string

	// Checksum is the content checksum ("sha256:..."), empty for
	// statements-only datasets.
	Checksum() string

	// Dir is the canonical-layout directory, empty for statements-only
	// datasets.
	Dir() string

	// Manifest returns the parsed manifest, nil for statements-only.
	Manifest() *Manifest

	// Schema describes every node and rel table with typed columns.
	// Schema-full engines (zu, Ladybug) generate DDL from it.
	Schema() Schema

	// NodeFiles returns the CSV files for a node label.
	NodeFiles(label string) ([]string, error)

	// RelFiles returns the CSV files for a relationship type.
	RelFiles(typ string) ([]string, error)

	// Params returns the curated parameter pool for a key, or nil when
	// the pool is absent.
	Params(key string) ([]map[string]Value, error)

	// Statements returns setup statements for statements-only datasets,
	// nil otherwise.
	Statements() []string
}

// Manifest is the dataset's recorded recipe and invariants.
type Manifest struct {
	Name             string            `json:"name"`
	Kind             string            `json:"kind"` // synthetic | ldbc | pinned | statements
	Generator        string            `json:"generator,omitempty"`
	GeneratorVersion string            `json:"generatorVersion,omitempty"`
	Seed             int64             `json:"seed,omitempty"`
	Params           map[string]string `json:"params,omitempty"`
	Scale            string            `json:"scale,omitempty"`
	ListDelimiter    string            `json:"listDelimiter"`
	Null             string            `json:"null"`
	Checksum         string            `json:"checksum,omitempty"`
	Invariants       Invariants        `json:"invariants"`
	SchemaDef        Schema            `json:"schema"`
}

// Invariants are generator-recorded ground truths oracles rely on.
type Invariants struct {
	NodeCount     int64  `json:"nodeCount"`
	EdgeCount     int64  `json:"edgeCount"`
	TriangleCount *int64 `json:"triangleCount,omitempty"`
	Diameter      *int64 `json:"diameter,omitempty"`
}

// Schema describes the typed tables of a dataset.
type Schema struct {
	Nodes map[string]NodeSchema `json:"nodes"`
	Rels  map[string]RelSchema  `json:"rels"`
}

// NodeSchema is one node table.
type NodeSchema struct {
	Files      []string `json:"files"`
	ID         Column   `json:"id"`
	Properties []Column `json:"properties"`
	Labels     []string `json:"labels,omitempty"`
}

// RelSchema is one relationship table.
type RelSchema struct {
	Files      []string `json:"files"`
	Start      string   `json:"start"` // start node label
	End        string   `json:"end"`   // end node label
	Properties []Column `json:"properties"`
}

// Column is a typed CSV column. Type is one of: ID, STRING, INT64,
// FLOAT64, BOOL, DATE, DATETIME, STRING[].
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}
