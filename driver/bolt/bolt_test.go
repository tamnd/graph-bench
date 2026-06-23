//go:build bolt

package bolt

import (
	"fmt"
	"testing"

	"github.com/tamnd/graph-bench/target"
)

// TestDecodeValuePrimitives proves the canonical type coercions work for the
// basic scalar types: bool, int variants, float variants, string, nil.
func TestDecodeValuePrimitives(t *testing.T) {
	check := func(in, want any) {
		t.Helper()
		got := decodeValue(in)
		gs := fmt.Sprintf("%T/%v", got, got)
		ws := fmt.Sprintf("%T/%v", want, want)
		if gs != ws {
			t.Errorf("decodeValue(%T %v) = %s, want %s", in, in, gs, ws)
		}
	}
	check(nil, nil)
	check(true, true)
	check(false, false)
	check(int64(42), int64(42))
	check(int(7), int64(7))
	check(int32(3), int64(3))
	check(float64(3.14), float64(3.14))
	check("hello", "hello")
}

// TestDecodeValueBytes proves []byte passes through as []byte.
func TestDecodeValueBytes(t *testing.T) {
	in := []byte("raw")
	out := decodeValue(in)
	b, ok := out.([]byte)
	if !ok {
		t.Fatalf("got %T, want []byte", out)
	}
	if string(b) != "raw" {
		t.Errorf("got %q, want raw", b)
	}
}

// TestDecodeValueSlice proves nested slices are decoded recursively.
func TestDecodeValueSlice(t *testing.T) {
	in := []any{int64(1), "two", true}
	out := decodeValue(in)
	sl, ok := out.([]target.Value)
	if !ok {
		t.Fatalf("got %T, want []Value", out)
	}
	if len(sl) != 3 {
		t.Fatalf("len=%d, want 3", len(sl))
	}
	if sl[0] != int64(1) {
		t.Errorf("sl[0]=%v, want int64(1)", sl[0])
	}
	if sl[1] != "two" {
		t.Errorf("sl[1]=%v, want two", sl[1])
	}
	if sl[2] != true {
		t.Errorf("sl[2]=%v, want true", sl[2])
	}
}

// TestDecodeValueMap proves map values are decoded recursively.
func TestDecodeValueMap(t *testing.T) {
	in := map[string]any{"x": int64(10), "y": "hello"}
	out := decodeValue(in)
	m, ok := out.(map[string]target.Value)
	if !ok {
		t.Fatalf("got %T, want map[string]Value", out)
	}
	if m["x"] != int64(10) {
		t.Errorf("m[x]=%v, want 10", m["x"])
	}
	if m["y"] != "hello" {
		t.Errorf("m[y]=%v, want hello", m["y"])
	}
}

// TestDecodeValueUnknown proves an unknown type passes through as-is so
// validation, not the decoder, catches the mismatch.
func TestDecodeValueUnknown(t *testing.T) {
	type myCustom struct{ V int }
	in := myCustom{V: 99}
	out := decodeValue(in)
	if out != in {
		t.Errorf("unknown type should pass through: got %v", out)
	}
}

// TestParamsToNeo4j proves nil params produces nil and non-nil params are
// copied.
func TestParamsToNeo4j(t *testing.T) {
	if got := paramsToNeo4j(nil); got != nil {
		t.Error("nil params should produce nil")
	}
	p := target.Params{"a": int64(1), "b": "two"}
	got := paramsToNeo4j(p)
	if len(got) != 2 {
		t.Errorf("len=%d, want 2", len(got))
	}
	if got["a"] != int64(1) {
		t.Errorf("a=%v, want 1", got["a"])
	}
}
