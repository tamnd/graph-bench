package workload

import (
	"testing"

	"github.com/tamnd/graph-bench/target"
)

func ans(cols []string, rows ...[]target.Value) *target.Answer {
	return &target.Answer{Columns: cols, Rows: rows}
}

func row(vs ...target.Value) []target.Value { return vs }

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
	// Same set, different row order: matches when unordered, fails when ordered.
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
	gotNode := target.Node{ID: "engine-a-17", Labels: []string{"Node"}, Props: map[string]target.Value{"id": int64(5)}}
	wantNode := target.Node{ID: "engine-b-99", Labels: []string{"Node"}, Props: map[string]target.Value{"id": int64(5)}}
	got := ans([]string{"n"}, row(gotNode))
	want := ans([]string{"n"}, row(wantNode))
	// Default excludes the element id: same labels and props, different id, matches.
	if err := Compare(got, want, CompareSpec{Ordered: true}); err != nil {
		t.Errorf("default should ignore element id: %v", err)
	}
	// IncludeElementIDs turns the id difference into a mismatch.
	if err := Compare(got, want, CompareSpec{Ordered: true, IncludeElementIDs: true}); err == nil {
		t.Error("IncludeElementIDs should compare the element id")
	}
}

func TestCompareNodePropertyMismatch(t *testing.T) {
	gotNode := target.Node{Labels: []string{"Node"}, Props: map[string]target.Value{"id": int64(5)}}
	wantNode := target.Node{Labels: []string{"Node"}, Props: map[string]target.Value{"id": int64(6)}}
	got := ans([]string{"n"}, row(gotNode))
	want := ans([]string{"n"}, row(wantNode))
	if err := Compare(got, want, CompareSpec{Ordered: true}); err == nil {
		t.Error("a differing property should fail even with ids excluded")
	}
}

func TestCompareRelationship(t *testing.T) {
	gotRel := target.Relationship{ID: "a1", Type: "EDGE", StartID: "x", EndID: "y", Props: map[string]target.Value{"w": int64(2)}}
	wantRel := target.Relationship{ID: "b2", Type: "EDGE", StartID: "p", EndID: "q", Props: map[string]target.Value{"w": int64(2)}}
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
}

func TestCompareList(t *testing.T) {
	got := ans([]string{"xs"}, row([]target.Value{int64(1), int64(2), int64(3)}))
	want := ans([]string{"xs"}, row([]target.Value{int64(1), int64(2), int64(3)}))
	if err := Compare(got, want, CompareSpec{Ordered: true}); err != nil {
		t.Errorf("equal lists: %v", err)
	}
	// List order is part of the value even in an unordered query.
	reordered := ans([]string{"xs"}, row([]target.Value{int64(3), int64(2), int64(1)}))
	if err := Compare(reordered, want, CompareSpec{Ordered: false}); err == nil {
		t.Error("list order is significant; a reordered list should fail")
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

func TestCompareNilAnswers(t *testing.T) {
	if err := Compare(nil, ans([]string{"n"}), CompareSpec{}); err == nil {
		t.Error("nil engine answer should error")
	}
	if err := Compare(ans([]string{"n"}), nil, CompareSpec{}); err == nil {
		t.Error("nil reference answer should error")
	}
}
