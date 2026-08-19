package zu2

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tamnd/graph-bench/engine"
)

// The primitive dialect: one line, one operation, no planner.
//
// zu2 is a storage engine and not a database. It has an indexed record
// read, an adjacency load and a few walks over them, and it has no
// parser, no plan and no optimizer. A benchmark still has to say which
// of those it wants, so this is the smallest thing that says it:
//
//	point key=$id as id
//	edge src=$src dst=$dst as found
//	degree out seed=$seed as n
//	khop out 2 seed=$seed as n
//	reach out 1..3 seed=$seed as n
//	sp out src=$src dst=$dst as d
//	count nodes as n
//
// A direction is out, in or both. A parameter is named with a $ and
// resolved out of the Op's params at execution time; a vertex is named
// by the key it was loaded under, which for every dataset here is the
// id column. The `as` clause names the result column, and it is required
// rather than derived from the verb: the column name is part of the
// question the workload asked, and two engines answering the same
// question have to spell it the same way or the comparison fails on the
// header rather than on the answer.
//
// The parse is strict. Every token has to be one this understands, in
// the order it expects, because the alternative is a text with a typo in
// it running some other query and reporting a number for it. A text this
// cannot parse is an error at prepare time and the run says so.

// verb is the operation a directive asks for.
type verb string

const (
	verbPoint  verb = "point"
	verbEdge   verb = "edge"
	verbDegree verb = "degree"
	verbKhop   verb = "khop"
	verbReach  verb = "reach"
	verbPath   verb = "sp"
	verbCount  verb = "count"
)

// direction is which way an edge is followed, in the numbering zu2.h
// gives: ZU2_OUT, ZU2_IN, ZU2_BOTH. The adapter passes it straight
// through, so these are not to be renumbered.
type direction int

const (
	dirOut  direction = 0
	dirIn   direction = 1
	dirBoth direction = 2
)

// directive is a parsed line of the primitive dialect.
type directive struct {
	verb verb
	dir  direction

	// depth is the k of a k-hop and the upper bound of a reach. The
	// lower bound of a reach is always one and the parser rejects any
	// other, because the walk underneath starts at the seed's
	// neighbours and cannot start anywhere else.
	depth uint32

	// The parameter names the operands come from, without the $. Which
	// of these are set depends on the verb.
	seed string
	src  string
	dst  string

	// column is the name the single result column is reported under.
	column string
}

// parse reads one line of the primitive dialect.
func parse(text string) (directive, error) {
	tokens := strings.Fields(text)
	if len(tokens) == 0 {
		return directive{}, fmt.Errorf("zu2: empty directive")
	}
	rest, column, err := takeColumn(tokens)
	if err != nil {
		return directive{}, err
	}
	d := directive{verb: verb(rest[0]), column: column}
	args := rest[1:]

	switch d.verb {
	case verbPoint:
		// No direction: a point read touches no edge.
		if len(args) != 1 {
			return directive{}, fmt.Errorf("zu2: point takes key=$param, got %q", text)
		}
		d.seed, err = binding(args[0], "key")
	case verbEdge:
		if len(args) != 2 {
			return directive{}, fmt.Errorf("zu2: edge takes src=$param dst=$param, got %q", text)
		}
		if d.src, err = binding(args[0], "src"); err == nil {
			d.dst, err = binding(args[1], "dst")
		}
	case verbDegree:
		if len(args) != 2 {
			return directive{}, fmt.Errorf("zu2: degree takes <direction> seed=$param, got %q", text)
		}
		if d.dir, err = way(args[0]); err == nil {
			d.seed, err = binding(args[1], "seed")
		}
	case verbKhop:
		if len(args) != 3 {
			return directive{}, fmt.Errorf("zu2: khop takes <direction> <k> seed=$param, got %q", text)
		}
		if d.dir, err = way(args[0]); err == nil {
			if d.depth, err = depth(args[1]); err == nil {
				d.seed, err = binding(args[2], "seed")
			}
		}
	case verbReach:
		if len(args) != 3 {
			return directive{}, fmt.Errorf("zu2: reach takes <direction> 1..<k> seed=$param, got %q", text)
		}
		if d.dir, err = way(args[0]); err == nil {
			if d.depth, err = span(args[1]); err == nil {
				d.seed, err = binding(args[2], "seed")
			}
		}
	case verbPath:
		if len(args) != 3 {
			return directive{}, fmt.Errorf("zu2: sp takes <direction> src=$param dst=$param, got %q", text)
		}
		if d.dir, err = way(args[0]); err == nil {
			if d.src, err = binding(args[1], "src"); err == nil {
				d.dst, err = binding(args[2], "dst")
			}
		}
	case verbCount:
		// Only one thing is countable without a scan operator, and
		// spelling it out leaves room for the next one.
		if len(args) != 1 || args[0] != "nodes" {
			return directive{}, fmt.Errorf("zu2: count takes nodes, got %q", text)
		}
	default:
		return directive{}, fmt.Errorf("zu2: %q is not an operation this engine has", rest[0])
	}
	if err != nil {
		return directive{}, fmt.Errorf("%w (in %q)", err, text)
	}
	return d, nil
}

// takeColumn splits the trailing `as <column>` off a directive.
func takeColumn(tokens []string) ([]string, string, error) {
	n := len(tokens)
	if n < 3 || tokens[n-2] != "as" {
		return nil, "", fmt.Errorf("zu2: a directive ends with `as <column>`, got %q", strings.Join(tokens, " "))
	}
	return tokens[:n-2], tokens[n-1], nil
}

// binding reads a `name=$param` token and returns the parameter name.
func binding(token, name string) (string, error) {
	prefix := name + "=$"
	if !strings.HasPrefix(token, prefix) || len(token) == len(prefix) {
		return "", fmt.Errorf("zu2: expected %s$param, got %q", name+"=", token)
	}
	return token[len(prefix):], nil
}

// way reads a direction word.
func way(token string) (direction, error) {
	switch token {
	case "out":
		return dirOut, nil
	case "in":
		return dirIn, nil
	case "both":
		return dirBoth, nil
	}
	return 0, fmt.Errorf("zu2: %q is not a direction (out, in, both)", token)
}

// depth reads a hop count, which has to be one or more: a zero-hop
// expansion is the seed itself and no workload asks for it.
func depth(token string) (uint32, error) {
	k, err := strconv.ParseUint(token, 10, 32)
	if err != nil || k == 0 {
		return 0, fmt.Errorf("zu2: %q is not a hop count of one or more", token)
	}
	return uint32(k), nil
}

// span reads a `1..k` range. The lower bound is checked rather than
// ignored: a text that says 2..3 is asking a question this cannot
// answer, and answering 1..3 instead would be a wrong number rather than
// a missing one.
func span(token string) (uint32, error) {
	lo, hi, ok := strings.Cut(token, "..")
	if !ok || lo != "1" {
		return 0, fmt.Errorf("zu2: %q is not a range of the form 1..k", token)
	}
	return depth(hi)
}

// key resolves a directive's operand to the vertex key it names. Keys
// are the bytes a vertex was loaded under, so an integer id and the
// string of it are the same vertex, and anything else in a pool is a
// pool the engine cannot read.
func key(params map[string]engine.Value, name string) (string, error) {
	v, ok := params[name]
	if !ok {
		return "", fmt.Errorf("zu2: no parameter %q in this operation", name)
	}
	switch t := v.(type) {
	case string:
		return t, nil
	case int:
		return strconv.Itoa(t), nil
	case int32:
		return strconv.FormatInt(int64(t), 10), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	}
	return "", fmt.Errorf("zu2: parameter %q is a %T, which is not a vertex key", name, v)
}

// idValue is what a point read reports for the vertex it found. zu2 has
// no properties: a vertex is its key, and the id column a Cypher engine
// returns is that key read back. It comes back as a number when it is
// one, because a reference computed off the same dataset says int64 and
// a column of decimal strings would fail the comparison on its type
// rather than on its content.
func idValue(k string) engine.Value {
	if n, err := strconv.ParseInt(k, 10, 64); err == nil {
		return n
	}
	return k
}
