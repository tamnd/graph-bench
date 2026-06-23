//go:build bolt

package memgraph

import (
	"strings"
	"testing"

	"github.com/tamnd/graph-bench/target"
)

// TestMemgraphName proves the Name() method returns the stable identifier.
func TestMemgraphName(t *testing.T) {
	tgt := New("bolt://127.0.0.1:7687", "", "")
	if tgt.Name() != "memgraph" {
		t.Errorf("Name()=%q, want memgraph", tgt.Name())
	}
}

// TestMemgraphPlane proves Memgraph is on the Bolt plane.
func TestMemgraphPlane(t *testing.T) {
	tgt := New("bolt://127.0.0.1:7687", "", "")
	if tgt.Plane() != target.Bolt {
		t.Errorf("Plane()=%v, want Bolt", tgt.Plane())
	}
}

// TestMemgraphCapabilities proves Cypher is listed and BulkCSVLoad is true.
func TestMemgraphCapabilities(t *testing.T) {
	tgt := New("bolt://127.0.0.1:7687", "", "")
	caps := tgt.Capabilities()
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
		t.Error("BulkCSVLoad should be true")
	}
}

// TestMemgraphBatchNode proves the batch builder produces a CREATE statement
// for nodes.
func TestMemgraphBatchNode(t *testing.T) {
	cols := []target.Column{
		{Name: "", Type: ""},
		{Name: "name", Type: "string"},
		{Name: "rank", Type: "int"},
	}
	rows := []string{"1,Alice,1", "2,Bob,2"}
	cypher := buildUnwindCypher(rows, cols, "Person", true)
	if !strings.Contains(cypher, "CREATE") {
		t.Errorf("node cypher should contain CREATE, got: %s", cypher)
	}
	if !strings.Contains(cypher, "Person") {
		t.Error("cypher should contain label Person")
	}
	if !strings.Contains(cypher, "UNWIND") {
		t.Error("cypher should contain UNWIND")
	}
}

// TestMemgraphBatchRel proves the batch builder produces a MERGE statement for
// relationships.
func TestMemgraphBatchRel(t *testing.T) {
	cols := []target.Column{
		{Name: "", Type: ""}, // :START_ID
		{Name: "", Type: ""}, // :END_ID
	}
	rows := []string{"1,2"}
	cypher := buildUnwindCypher(rows, cols, "KNOWS", false)
	if !strings.Contains(cypher, "MERGE") {
		t.Errorf("rel cypher should contain MERGE, got: %s", cypher)
	}
	if !strings.Contains(cypher, "KNOWS") {
		t.Error("cypher should contain type KNOWS")
	}
}

// TestMemgraphBatchEmpty proves an empty batch returns an empty string.
func TestMemgraphBatchEmpty(t *testing.T) {
	cols := []target.Column{{Name: "x", Type: "string"}}
	if got := buildUnwindCypher(nil, cols, "X", true); got != "" {
		t.Errorf("empty rows should return empty string, got %q", got)
	}
}
