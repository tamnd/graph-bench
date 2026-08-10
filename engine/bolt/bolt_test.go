//go:build bolt

package bolt

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v6/neo4j/dbtype"

	"github.com/tamnd/graph-bench/engine"
)

func TestDecodeScalars(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want engine.Value
	}{
		{"nil", nil, nil},
		{"bool", true, true},
		{"int64", int64(42), int64(42)},
		{"int", int(7), int64(7)},
		{"int32", int32(-3), int64(-3)},
		{"int16", int16(9), int64(9)},
		{"int8", int8(1), int64(1)},
		{"float64", 3.5, 3.5},
		{"float32", float32(1.5), 1.5},
		{"string", "hi", "hi"},
		{"bytes", []byte{1, 2}, []byte{1, 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeValue(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("decodeValue(%v) = %#v (%T), want %#v (%T)", tt.in, got, got, tt.want, tt.want)
			}
		})
	}
}

func TestDecodeCollections(t *testing.T) {
	got := decodeValue([]any{int32(1), "a", []any{float32(0.5)}})
	want := []engine.Value{int64(1), "a", []engine.Value{0.5}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("list: got %#v, want %#v", got, want)
	}

	gotM := decodeValue(map[string]any{"n": int(2), "s": "x"})
	wantM := map[string]engine.Value{"n": int64(2), "s": "x"}
	if !reflect.DeepEqual(gotM, wantM) {
		t.Errorf("map: got %#v, want %#v", gotM, wantM)
	}
}

func TestDecodeNode(t *testing.T) {
	in := dbtype.Node{
		ElementId: "4:abc:1",
		Labels:    []string{"Person"},
		Props:     map[string]any{"id": int64(9), "age": int32(30)},
	}
	got, ok := decodeValue(in).(engine.Node)
	if !ok {
		t.Fatalf("decodeValue(Node) = %T, want engine.Node", decodeValue(in))
	}
	if got.ID != "4:abc:1" || !reflect.DeepEqual(got.Labels, []string{"Person"}) {
		t.Errorf("node identity: %+v", got)
	}
	if got.Props["id"] != int64(9) || got.Props["age"] != int64(30) {
		t.Errorf("node props not normalized: %#v", got.Props)
	}
}

func TestDecodeRelationship(t *testing.T) {
	in := dbtype.Relationship{
		ElementId:      "5:abc:2",
		StartElementId: "4:abc:1",
		EndElementId:   "4:abc:3",
		Type:           "KNOWS",
		Props:          map[string]any{"since": int32(2020)},
	}
	got, ok := decodeValue(in).(engine.Rel)
	if !ok {
		t.Fatalf("decodeValue(Relationship) = %T, want engine.Rel", decodeValue(in))
	}
	if got.ID != "5:abc:2" || got.Type != "KNOWS" || got.Start != "4:abc:1" || got.End != "4:abc:3" {
		t.Errorf("rel identity: %+v", got)
	}
	if got.Props["since"] != int64(2020) {
		t.Errorf("rel props not normalized: %#v", got.Props)
	}
}

func TestDecodePath(t *testing.T) {
	in := dbtype.Path{
		Nodes: []dbtype.Node{
			{ElementId: "n0", Labels: []string{"Node"}, Props: map[string]any{}},
			{ElementId: "n1", Labels: []string{"Node"}, Props: map[string]any{}},
		},
		Relationships: []dbtype.Relationship{
			{ElementId: "r0", StartElementId: "n0", EndElementId: "n1", Type: "EDGE", Props: map[string]any{}},
		},
	}
	got, ok := decodeValue(in).(engine.Path)
	if !ok {
		t.Fatalf("decodeValue(Path) = %T, want engine.Path", decodeValue(in))
	}
	if len(got.Nodes) != 2 || len(got.Rels) != 1 {
		t.Fatalf("path shape: %d nodes, %d rels", len(got.Nodes), len(got.Rels))
	}
	if got.Nodes[0].ID != "n0" || got.Rels[0].Type != "EDGE" {
		t.Errorf("path content: %+v", got)
	}
}

func TestDecodeTemporal(t *testing.T) {
	ref := time.Date(2026, 8, 10, 12, 30, 0, 0, time.UTC)
	for name, in := range map[string]any{
		"date":          dbtype.Date(ref),
		"localdatetime": dbtype.LocalDateTime(ref),
		"localtime":     dbtype.LocalTime(ref),
		"time":          dbtype.Time(ref),
	} {
		got, ok := decodeValue(in).(time.Time)
		if !ok {
			t.Errorf("%s: decoded to %T, want time.Time", name, decodeValue(in))
			continue
		}
		if !got.Equal(ref) {
			t.Errorf("%s: got %v, want %v", name, got, ref)
		}
	}
	// DATETIME arrives as time.Time already and passes through.
	if got := decodeValue(ref); got != ref {
		t.Errorf("datetime passthrough: got %v", got)
	}
}

func TestDecodeVector(t *testing.T) {
	tests := []struct {
		name string
		in   any
	}{
		{"float64", dbtype.Vector[float64]{Elems: []float64{1, 2.5}}},
		{"float32", dbtype.Vector[float32]{Elems: []float32{1, 2.5}}},
		{"int8", dbtype.Vector[int8]{Elems: []int8{1, 2}}},
		{"int16", dbtype.Vector[int16]{Elems: []int16{1, 2}}},
		{"int32", dbtype.Vector[int32]{Elems: []int32{1, 2}}},
		{"int64", dbtype.Vector[int64]{Elems: []int64{1, 2}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := decodeValue(tt.in).([]float64)
			if !ok {
				t.Fatalf("decodeValue(Vector[%s]) = %T, want []float64", tt.name, decodeValue(tt.in))
			}
			if len(got) != 2 || got[0] != 1 {
				t.Errorf("vector content: %v", got)
			}
		})
	}
}

func TestParseHeader(t *testing.T) {
	cols := ParseHeader("id:ID,:LABEL", nil)
	want := []Column{{Name: "id", Type: "ID"}, {Name: "", Type: "LABEL"}}
	if !reflect.DeepEqual(cols, want) {
		t.Errorf("node header: got %#v, want %#v", cols, want)
	}

	cols = ParseHeader(":START_ID,:END_ID,weight:FLOAT64,name", map[string]string{"name": "STRING"})
	want = []Column{
		{Name: "", Type: "START_ID"},
		{Name: "", Type: "END_ID"},
		{Name: "weight", Type: "FLOAT64"},
		{Name: "name", Type: "STRING"},
	}
	if !reflect.DeepEqual(cols, want) {
		t.Errorf("rel header: got %#v, want %#v", cols, want)
	}

	// Unannotated column absent from propTypes defaults to STRING.
	cols = ParseHeader("mystery", nil)
	if cols[0].Type != "STRING" {
		t.Errorf("default type: got %q, want STRING", cols[0].Type)
	}
}

func TestColumnStructural(t *testing.T) {
	for _, typ := range []string{"ID", "START_ID", "END_ID", "LABEL", "TYPE"} {
		if !(Column{Type: typ}).Structural() {
			t.Errorf("%s should be structural", typ)
		}
	}
	for _, typ := range []string{"STRING", "INT64", "FLOAT64", "BOOL", "DATE", "DATETIME", "STRING[]"} {
		if (Column{Type: typ}).Structural() {
			t.Errorf("%s should not be structural", typ)
		}
	}
}

func TestReadCSV(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/n.csv"
	if err := writeFile(path, "id:ID,:LABEL\n0,Node\n1,Node\n"); err != nil {
		t.Fatal(err)
	}
	cols, rows, err := ReadCSV(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 2 || len(rows) != 2 || rows[1] != "1,Node" {
		t.Errorf("got cols=%v rows=%v", cols, rows)
	}

	// Empty file: no columns, no rows, no error.
	empty := dir + "/e.csv"
	if err := writeFile(empty, ""); err != nil {
		t.Fatal(err)
	}
	cols, rows, err = ReadCSV(empty, nil)
	if err != nil || cols != nil || rows != nil {
		t.Errorf("empty file: cols=%v rows=%v err=%v", cols, rows, err)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
