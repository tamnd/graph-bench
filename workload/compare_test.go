package workload

import (
	"testing"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

func ans(cols []string, rows ...[]engine.Value) *Answer {
	return &Answer{Columns: cols, Rows: rows}
}

func row(vs ...engine.Value) []engine.Value { return vs }

func TestCompareSpecFloatTolerance(t *testing.T) {
	if got := (CompareSpec{}).FloatTolerance(); got != 1e-9 {
		t.Errorf("default FloatTolerance = %g, want 1e-9", got)
	}
	if got := (CompareSpec{FloatTol: 1e-3}).FloatTolerance(); got != 1e-3 {
		t.Errorf("FloatTolerance = %g, want 1e-3", got)
	}
}

func TestCompareEqualScalars(t *testing.T) {
	got := ans([]string{"n"}, row(int64(3)), row(int64(7)))
	want := ans([]string{"n"}, row(int64(3)), row(int64(7)))
	if err := Compare(got, want, CompareSpec{Ordered: true}); err != nil {
		t.Errorf("equal scalars: %v", err)
	}
}

func TestCompareColumnMismatch(t *testing.T) {
	got := ans([]string{"a"}, row(int64(1)))
	want := ans([]string{"b"}, row(int64(1)))
	if err := Compare(got, want, CompareSpec{}); err == nil {
		t.Error("column mismatch not caught")
	}
}

func TestCompareRowCountMismatch(t *testing.T) {
	got := ans([]string{"n"}, row(int64(1)))
	want := ans([]string{"n"}, row(int64(1)), row(int64(2)))
	if err := Compare(got, want, CompareSpec{}); err == nil {
		t.Error("row count mismatch not caught")
	}
}

func TestCompareUnorderedMatches(t *testing.T) {
	// Same set, different row order: matches when unordered, fails when
	// ordered.
	got := ans([]string{"id"}, row(int64(2)), row(int64(1)), row(int64(3)))
	want := ans([]string{"id"}, row(int64(1)), row(int64(2)), row(int64(3)))
	if err := Compare(got, want, CompareSpec{Ordered: false}); err != nil {
		t.Errorf("unordered should match a reordered set: %v", err)
	}
	if err := Compare(got, want, CompareSpec{Ordered: true}); err == nil {
		t.Error("ordered should reject a reordered set")
	}
}

func TestCompareUnorderedDetectsMissingRow(t *testing.T) {
	got := ans([]string{"id"}, row(int64(1)), row(int64(2)), row(int64(2)))
	want := ans([]string{"id"}, row(int64(1)), row(int64(2)), row(int64(3)))
	if err := Compare(got, want, CompareSpec{Ordered: false}); err == nil {
		t.Error("unordered should catch a different multiset")
	}
}

func TestCompareFloatTolerance(t *testing.T) {
	got := ans([]string{"avg"}, row(2.0000000001))
	want := ans([]string{"avg"}, row(2.0))
	if err := Compare(got, want, CompareSpec{Ordered: true}); err != nil {
		t.Errorf("within default tolerance should match: %v", err)
	}
	got2 := ans([]string{"avg"}, row(2.5))
	if err := Compare(got2, want, CompareSpec{Ordered: true}); err == nil {
		t.Error("outside tolerance should fail")
	}
}

func TestCompareNumericCoercion(t *testing.T) {
	// A count reference of int64 against an engine that returns a float.
	got := ans([]string{"c"}, row(float64(42)))
	want := ans([]string{"c"}, row(int64(42)))
	if err := Compare(got, want, CompareSpec{Ordered: true}); err == nil {
		t.Error("strict mode should reject int-vs-float without coercion")
	}
	if err := Compare(got, want, CompareSpec{Ordered: true, CoerceNum: true}); err != nil {
		t.Errorf("coercion should let int64 match float64: %v", err)
	}
}

func TestCompareNodeExcludesElementID(t *testing.T) {
	gotNode := engine.Node{ID: "engine-a-17", Labels: []string{"Node"}, Props: map[string]engine.Value{"id": int64(5)}}
	wantNode := engine.Node{ID: "engine-b-99", Labels: []string{"Node"}, Props: map[string]engine.Value{"id": int64(5)}}
	got := ans([]string{"n"}, row(gotNode))
	want := ans([]string{"n"}, row(wantNode))
	// Default excludes the element id: same labels and props, different id,
	// matches.
	if err := Compare(got, want, CompareSpec{Ordered: true}); err != nil {
		t.Errorf("default should ignore element id: %v", err)
	}
	// IncludeElementIDs turns the id difference into a mismatch.
	if err := Compare(got, want, CompareSpec{Ordered: true, IncludeElementIDs: true}); err == nil {
		t.Error("IncludeElementIDs should compare the element id")
	}
}

func TestCompareNodePropertyMismatch(t *testing.T) {
	gotNode := engine.Node{Labels: []string{"Node"}, Props: map[string]engine.Value{"id": int64(5)}}
	wantNode := engine.Node{Labels: []string{"Node"}, Props: map[string]engine.Value{"id": int64(6)}}
	got := ans([]string{"n"}, row(gotNode))
	want := ans([]string{"n"}, row(wantNode))
	if err := Compare(got, want, CompareSpec{Ordered: true}); err == nil {
		t.Error("a differing property should fail even with ids excluded")
	}
}

func TestCompareUnorderedNodeRows(t *testing.T) {
	// Node-valued rows in different orders with engine-specific element ids:
	// the canonical row key must exclude the ids (like the comparison does)
	// so the sort pairs the logically equal rows up.
	n1a := engine.Node{ID: "a-1", Labels: []string{"Node"}, Props: map[string]engine.Value{"id": int64(1)}}
	n2a := engine.Node{ID: "a-2", Labels: []string{"Node"}, Props: map[string]engine.Value{"id": int64(2)}}
	n1b := engine.Node{ID: "b-9", Labels: []string{"Node"}, Props: map[string]engine.Value{"id": int64(1)}}
	n2b := engine.Node{ID: "b-8", Labels: []string{"Node"}, Props: map[string]engine.Value{"id": int64(2)}}
	got := ans([]string{"n"}, row(n2a), row(n1a))
	want := ans([]string{"n"}, row(n1b), row(n2b))
	if err := Compare(got, want, CompareSpec{}); err != nil {
		t.Errorf("unordered node rows should match under id exclusion: %v", err)
	}
}

func TestCompareRelationship(t *testing.T) {
	gotRel := engine.Rel{ID: "a1", Type: "EDGE", Start: "x", End: "y", Props: map[string]engine.Value{"w": int64(2)}}
	wantRel := engine.Rel{ID: "b2", Type: "EDGE", Start: "p", End: "q", Props: map[string]engine.Value{"w": int64(2)}}
	got := ans([]string{"r"}, row(gotRel))
	want := ans([]string{"r"}, row(wantRel))
	if err := Compare(got, want, CompareSpec{Ordered: true}); err != nil {
		t.Errorf("default should compare type and props only: %v", err)
	}
	// Type mismatch always fails.
	gotRel2 := gotRel
	gotRel2.Type = "OTHER"
	if err := Compare(ans([]string{"r"}, row(gotRel2)), want, CompareSpec{Ordered: true}); err == nil {
		t.Error("a differing rel type should fail")
	}
	// Endpoint ids only matter when the spec includes element ids.
	if err := Compare(got, want, CompareSpec{Ordered: true, IncludeElementIDs: true}); err == nil {
		t.Error("IncludeElementIDs should compare endpoints")
	}
}

func TestComparePath(t *testing.T) {
	mkPath := func(elemPrefix string) engine.Path {
		return engine.Path{
			Nodes: []engine.Node{
				{ID: elemPrefix + "1", Labels: []string{"Node"}, Props: map[string]engine.Value{"id": int64(1)}},
				{ID: elemPrefix + "2", Labels: []string{"Node"}, Props: map[string]engine.Value{"id": int64(2)}},
			},
			Rels: []engine.Rel{
				{ID: elemPrefix + "r", Type: "EDGE", Props: map[string]engine.Value{}},
			},
		}
	}
	got := ans([]string{"p"}, row(mkPath("a-")))
	want := ans([]string{"p"}, row(mkPath("b-")))
	if err := Compare(got, want, CompareSpec{Ordered: true}); err != nil {
		t.Errorf("paths equal up to element ids should match: %v", err)
	}
	// A wrong node property inside the path fails.
	bad := mkPath("a-")
	bad.Nodes[1].Props["id"] = int64(3)
	if err := Compare(ans([]string{"p"}, row(bad)), want, CompareSpec{Ordered: true}); err == nil {
		t.Error("a differing path node property should fail")
	}
}

func TestCompareList(t *testing.T) {
	got := ans([]string{"xs"}, row([]engine.Value{int64(1), int64(2), int64(3)}))
	want := ans([]string{"xs"}, row([]engine.Value{int64(1), int64(2), int64(3)}))
	if err := Compare(got, want, CompareSpec{Ordered: true}); err != nil {
		t.Errorf("equal lists: %v", err)
	}
	// List order is part of the value even in an unordered query.
	reordered := ans([]string{"xs"}, row([]engine.Value{int64(3), int64(2), int64(1)}))
	if err := Compare(reordered, want, CompareSpec{Ordered: false}); err == nil {
		t.Error("list order is significant; a reordered list should fail")
	}
}

func TestCompareMap(t *testing.T) {
	got := ans([]string{"m"}, row(map[string]engine.Value{"a": int64(1), "b": "x"}))
	want := ans([]string{"m"}, row(map[string]engine.Value{"b": "x", "a": int64(1)}))
	if err := Compare(got, want, CompareSpec{Ordered: true}); err != nil {
		t.Errorf("equal maps regardless of key order: %v", err)
	}
	missing := ans([]string{"m"}, row(map[string]engine.Value{"a": int64(1)}))
	if err := Compare(missing, want, CompareSpec{Ordered: true}); err == nil {
		t.Error("a missing map key should fail")
	}
}

func TestCompareNullAndBoolAndString(t *testing.T) {
	got := ans([]string{"a", "b", "c"}, row(nil, true, "hi"))
	want := ans([]string{"a", "b", "c"}, row(nil, true, "hi"))
	if err := Compare(got, want, CompareSpec{Ordered: true}); err != nil {
		t.Errorf("null/bool/string equal: %v", err)
	}
	bad := ans([]string{"a", "b", "c"}, row(int64(0), true, "hi"))
	if err := Compare(bad, want, CompareSpec{Ordered: true}); err == nil {
		t.Error("0 is not null")
	}
}

func TestCompareTime(t *testing.T) {
	utc := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	// The same instant spelled in a different zone is equal.
	shifted := utc.In(time.FixedZone("plus2", 2*3600))
	got := ans([]string{"t"}, row(shifted))
	want := ans([]string{"t"}, row(utc))
	if err := Compare(got, want, CompareSpec{Ordered: true}); err != nil {
		t.Errorf("same instant should match across zones: %v", err)
	}
	later := ans([]string{"t"}, row(utc.Add(time.Second)))
	if err := Compare(later, want, CompareSpec{Ordered: true}); err == nil {
		t.Error("a different instant should fail")
	}
}

func TestCompareNilAnswers(t *testing.T) {
	if err := Compare(nil, ans([]string{"n"}), CompareSpec{}); err == nil {
		t.Error("nil engine answer should error")
	}
	if err := Compare(ans([]string{"n"}), nil, CompareSpec{}); err == nil {
		t.Error("nil reference answer should error")
	}
}
