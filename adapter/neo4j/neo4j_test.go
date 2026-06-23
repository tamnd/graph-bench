//go:build bolt

package neo4j

import (
	"testing"

	"github.com/tamnd/graph-bench/target"
)

// TestNeo4jName proves the Name() method returns the stable identifier.
func TestNeo4jName(t *testing.T) {
	tgt := New("bolt://127.0.0.1:7687", "", "")
	if tgt.Name() != "neo4j" {
		t.Errorf("Name()=%q, want neo4j", tgt.Name())
	}
}

// TestNeo4jPlane proves Setup is on the Bolt plane.
func TestNeo4jPlane(t *testing.T) {
	tgt := New("bolt://127.0.0.1:7687", "", "")
	if tgt.Plane() != target.Bolt {
		t.Errorf("Plane()=%v, want Bolt", tgt.Plane())
	}
}

// TestNeo4jCapabilities proves Cypher is listed and BulkCSVLoad is true.
func TestNeo4jCapabilities(t *testing.T) {
	tgt := New("bolt://127.0.0.1:7687", "", "")
	caps := tgt.Capabilities()
	if len(caps.Languages) == 0 {
		t.Error("Languages should not be empty")
	}
	found := false
	for _, l := range caps.Languages {
		if l == target.Cypher {
			found = true
		}
	}
	if !found {
		t.Error("Cypher should be in Languages")
	}
	if !caps.BulkCSVLoad {
		t.Error("BulkCSVLoad should be true for Neo4j")
	}
	if !caps.Transactions {
		t.Error("Transactions should be true for Neo4j")
	}
}

// TestBuildInsertBatchNode proves insertBatch builds a valid node CREATE for a
// simple row, without actually connecting to Neo4j.
func TestBuildInsertBatchNode(t *testing.T) {
	// We test the batch-building logic by checking it doesn't crash or produce
	// obviously wrong output. The actual Cypher is validated by the integration
	// test that runs against a container.
	cols := []target.Column{
		{Name: "", Type: ""}, // :ID structural column (skipped)
		{Name: "name", Type: "string"},
		{Name: "age", Type: "int"},
	}
	rows := []string{"1,Alice,30", "2,Bob,25"}
	// insertBatch needs a *neoDriver, which needs a *bolt.Pool, which needs a
	// running server. So we test the formatting side only, by calling a simpler
	// helper that doesn't need the driver.
	cypher := buildUnwindCypher(rows, cols, "Person", true)
	if cypher == "" {
		t.Error("buildUnwindCypher returned empty string")
	}
	// Must contain the label and the UNWIND keyword.
	if !contains(cypher, "UNWIND") {
		t.Error("cypher should contain UNWIND")
	}
	if !contains(cypher, "Person") {
		t.Error("cypher should contain label Person")
	}
	if !contains(cypher, "CREATE") {
		t.Error("cypher should contain CREATE")
	}
}

// TestBuildInsertBatchRel proves insertBatch builds a valid rel CREATE.
func TestBuildInsertBatchRel(t *testing.T) {
	cols := []target.Column{
		{Name: "", Type: ""}, // :START_ID
		{Name: "", Type: ""}, // :END_ID
		{Name: "weight", Type: "double"},
	}
	rows := []string{"1,2,0.5"}
	cypher := buildUnwindCypher(rows, cols, "KNOWS", false)
	if !contains(cypher, "MERGE") {
		t.Error("rel cypher should use MERGE for node lookup")
	}
	if !contains(cypher, "KNOWS") {
		t.Error("rel cypher should contain type KNOWS")
	}
}

// TestBuildInsertBatchEmpty proves an empty batch returns an empty string.
func TestBuildInsertBatchEmpty(t *testing.T) {
	cols := []target.Column{{Name: "x", Type: "string"}}
	cypher := buildUnwindCypher(nil, cols, "X", true)
	if cypher != "" {
		t.Errorf("empty batch should return empty string, got %q", cypher)
	}
}
