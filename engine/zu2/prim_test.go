package zu2

import (
	"strings"
	"testing"

	"github.com/tamnd/graph-bench/engine"
)

// The parser is where a benchmark quietly measures the wrong thing, so
// these check the accepted shapes exactly and check that the near misses
// are refused rather than rounded to something close.
func TestParseAccepts(t *testing.T) {
	for _, tc := range []struct {
		text string
		want directive
	}{
		{"point key=$id as id", directive{verb: verbPoint, seed: "id", column: "id"}},
		{"edge src=$src dst=$dst as found", directive{verb: verbEdge, src: "src", dst: "dst", column: "found"}},
		{"degree out seed=$seed as n", directive{verb: verbDegree, dir: dirOut, seed: "seed", column: "n"}},
		{"degree in seed=$seed as n", directive{verb: verbDegree, dir: dirIn, seed: "seed", column: "n"}},
		{"degree both seed=$seed as n", directive{verb: verbDegree, dir: dirBoth, seed: "seed", column: "n"}},
		{"khop out 2 seed=$seed as n", directive{verb: verbKhop, dir: dirOut, depth: 2, seed: "seed", column: "n"}},
		{"reach out 1..3 seed=$seed as n", directive{verb: verbReach, dir: dirOut, depth: 3, seed: "seed", column: "n"}},
		{"sp both src=$a dst=$b as d", directive{verb: verbPath, dir: dirBoth, src: "a", dst: "b", column: "d"}},
		{"count nodes as n", directive{verb: verbCount, column: "n"}},
	} {
		got, err := parse(tc.text)
		if err != nil {
			t.Errorf("parse(%q): %v", tc.text, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parse(%q) = %+v, want %+v", tc.text, got, tc.want)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	for _, text := range []string{
		"",
		"khop out 2 seed=$seed",             // no column
		"khop out 2 seed=$seed as",          // no column name
		"khop out 2 seed=$seed n",           // no as
		"khop sideways 2 seed=$seed as n",   // not a direction
		"khop out two seed=$seed as n",      // not a number
		"khop out 0 seed=$seed as n",        // a nought hop expansion
		"khop out 2 seed=seed as n",         // not a parameter
		"khop out 2 seed=$ as n",            // an empty parameter name
		"khop out 2 from=$seed as n",        // the wrong operand name
		"khop out seed=$seed as n",          // no depth
		"reach out 2..3 seed=$seed as n",    // a lower bound this cannot walk
		"reach out 3 seed=$seed as n",       // not a range
		"degree out seed=$seed extra as n",  // a token too many
		"point out key=$id as id",           // a point read has no direction
		"sp out src=$a as d",                // one endpoint
		"count edges as n",                  // not countable here
		"expand out 2 seed=$seed as n",      // not an operation
		"MATCH (n:Node {id: $id}) RETURN n", // a query language
	} {
		if got, err := parse(text); err == nil {
			t.Errorf("parse(%q) = %+v, want an error", text, got)
		}
	}
}

// A refusal has to say what was wrong with the text, because the run
// that hits one is a run that produced no number for that query.
func TestParseErrorNamesTheText(t *testing.T) {
	_, err := parse("khop sideways 2 seed=$seed as n")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "sideways") {
		t.Errorf("error %q does not name what was wrong", err)
	}
}

func TestKeyReadsTheParameterTypesAPoolCarries(t *testing.T) {
	params := map[string]engine.Value{
		"s": "42",
		"i": 42,
		"n": int64(42),
		"f": 42.0,
	}
	for _, name := range []string{"s", "i", "n"} {
		got, err := key(params, name)
		if err != nil {
			t.Errorf("key(%q): %v", name, err)
			continue
		}
		if got != "42" {
			t.Errorf("key(%q) = %q, want \"42\"", name, got)
		}
	}
	// A float is refused rather than formatted: 42.0 and 42 are the same
	// number and different keys, and guessing which one a pool meant is
	// how an engine ends up reporting misses as a result.
	if _, err := key(params, "f"); err == nil {
		t.Error("key of a float should be an error")
	}
	if _, err := key(params, "absent"); err == nil {
		t.Error("key of a parameter that is not there should be an error")
	}
}

func TestIDValueIsANumberWhenTheKeyIsOne(t *testing.T) {
	if got := idValue("42"); got != int64(42) {
		t.Errorf("idValue(\"42\") = %v (%T), want int64 42", got, got)
	}
	if got := idValue("comment-7"); got != "comment-7" {
		t.Errorf("idValue(\"comment-7\") = %v, want the string back", got)
	}
}
