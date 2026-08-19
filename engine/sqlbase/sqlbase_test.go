package sqlbase

import (
	"strings"
	"testing"

	"github.com/tamnd/graph-bench/engine"
)

// qmark and dollar are the two placeholder styles the real drivers use.
type qmark struct{ Driver }

func (qmark) Placeholder(int) string { return "?" }

type dollar struct{ Driver }

func (dollar) Placeholder(i int) string { return "$" + string(rune('0'+i)) }

func TestRewriteBothPlaceholderStyles(t *testing.T) {
	const text = `SELECT count(*) AS n FROM edge WHERE src = CAST($seed AS BIGINT)`
	params := map[string]engine.Value{"seed": "42"}

	got, args, err := Rewrite(qmark{}, text, params)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if want := `SELECT count(*) AS n FROM edge WHERE src = CAST(? AS BIGINT)`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if len(args) != 1 || args[0] != int64(42) {
		t.Errorf("args = %v, want [42]", args)
	}

	got, _, err = Rewrite(dollar{}, text, params)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if want := `SELECT count(*) AS n FROM edge WHERE src = CAST($1 AS BIGINT)`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A parameter used twice is passed twice, in the order the driver reads
// them, which is the whole reason the rewrite counts rather than caches.
func TestRewriteNumbersEveryUseInOrder(t *testing.T) {
	text := `SELECT $a, $b, $a`
	got, args, err := Rewrite(dollar{}, text, map[string]engine.Value{"a": 1, "b": 2})
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if want := `SELECT $1, $2, $3`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	want := []any{int64(1), int64(2), int64(1)}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("args[%d] = %v, want %v", i, args[i], want[i])
		}
	}
}

// An unbound parameter is an error and not an empty string, because a
// query that silently reads NULL where the seed should be still returns
// rows and still gets timed.
func TestRewriteRefusesWhatItCannotBind(t *testing.T) {
	for _, tc := range []struct {
		text   string
		params map[string]engine.Value
		want   string
	}{
		{`SELECT $seed`, nil, `no parameter "seed" was bound`},
		{`SELECT $`, nil, "bare $"},
		{`SELECT $v`, map[string]engine.Value{"v": []string{"a"}}, "cannot bind"},
	} {
		_, _, err := Rewrite(qmark{}, tc.text, tc.params)
		if err == nil {
			t.Errorf("Rewrite(%q): want an error", tc.text)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Rewrite(%q) = %v, want it to mention %q", tc.text, err, tc.want)
		}
	}
}

// An id written as a string in a pool is bound as an integer, because the
// id columns are BIGINT and PostgreSQL will not compare one to text.
func TestArgValueReadsAnIntegerOutOfAKey(t *testing.T) {
	for _, tc := range []struct {
		in   engine.Value
		want any
	}{
		{"42", int64(42)},
		{42, int64(42)},
		{int64(42), int64(42)},
		{"comment-7", "comment-7"},
		{1.5, 1.5},
		{nil, nil},
	} {
		got, err := argValue(tc.in)
		if err != nil {
			t.Errorf("argValue(%v): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("argValue(%v) = %v (%T), want %v (%T)", tc.in, got, got, tc.want, tc.want)
		}
	}
}

func TestSplitAnnotation(t *testing.T) {
	for _, tc := range []struct {
		alias string
		name  string
		conv  conversion
		bad   bool
	}{
		{alias: "n", name: "n", conv: convNone},
		{alias: "found::bool", name: "found", conv: convBool},
		{alias: "d::int", bad: true},
	} {
		name, conv, err := splitAnnotation(tc.alias)
		if tc.bad {
			if err == nil {
				t.Errorf("splitAnnotation(%q): want an error", tc.alias)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitAnnotation(%q): %v", tc.alias, err)
			continue
		}
		if name != tc.name || conv != tc.conv {
			t.Errorf("splitAnnotation(%q) = %q/%v, want %q/%v", tc.alias, name, conv, tc.name, tc.conv)
		}
	}
}

// SQLite answers an existence probe with 1 and PostgreSQL answers it with
// true. The reference says true, and the comparison is by type.
func TestDecodeBoolTakesAnIntegerOrABoolean(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want engine.Value
	}{
		{int64(1), true},
		{int64(0), false},
		{true, true},
		{nil, nil},
	} {
		got, err := decode(tc.in, convBool)
		if err != nil {
			t.Errorf("decode(%v): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("decode(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if _, err := decode("yes", convBool); err == nil {
		t.Error("decode of a string as a bool should be an error")
	}
}

// Widths normalize at the boundary so that every engine's count is an
// int64 and no comparison turns on which width a driver chose.
func TestDecodeNormalizesWidths(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want engine.Value
	}{
		{int32(7), int64(7)},
		{int(7), int64(7)},
		{float32(0.5), 0.5},
		{[]byte("x"), "x"},
	} {
		got, err := decode(tc.in, convNone)
		if err != nil {
			t.Errorf("decode(%v): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("decode(%v) = %v (%T), want %v (%T)", tc.in, got, got, tc.want, tc.want)
		}
	}
}
