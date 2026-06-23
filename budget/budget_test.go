package budget

import (
	"testing"
	"time"
)

// TestForBoundedClasses checks that the four bounded classes each return a
// non-zero ceiling from the default budget.
func TestForBoundedClasses(t *testing.T) {
	for _, c := range []Class{PointRead, Traversal, Subgraph, Write} {
		ceil := For(c)
		if !ceil.Bounded() {
			t.Errorf("For(%v).Bounded() = false, want true", c)
		}
		if ceil.P99 == 0 {
			t.Errorf("For(%v).P99 = 0, want a non-zero ceiling", c)
		}
	}
}

// TestForAnalyticalUnbounded checks that the analytical class is reported as
// unbounded so the gate skips an absolute latency check for it.
func TestForAnalyticalUnbounded(t *testing.T) {
	ceil := For(Analytical)
	if ceil.Bounded() {
		t.Errorf("For(Analytical).Bounded() = true, want false (analytical is unbounded)")
	}
	if ceil.P99 != 0 {
		t.Errorf("For(Analytical).P99 = %v, want 0", ceil.P99)
	}
}

// TestForUnknownClassUnbounded checks that an out-of-range class returns a zero
// ceiling so an unrecognized class is never gated against a latency it was not
// given.
func TestForUnknownClassUnbounded(t *testing.T) {
	unknown := Class(999)
	ceil := For(unknown)
	if ceil.Bounded() {
		t.Errorf("For(unknown).Bounded() = true, want false")
	}
}

// TestCeilingOrderPointRead proves the point-read ceiling is the tightest
// (fastest expected), because it is an index seek.
func TestCeilingOrderPointRead(t *testing.T) {
	if For(PointRead).P99 >= For(Traversal).P99 {
		t.Errorf("PointRead ceiling (%v) should be tighter than Traversal (%v)",
			For(PointRead).P99, For(Traversal).P99)
	}
}

// TestCeilingValuesInExpectedRange sanity-checks that the default ceilings land
// in the ranges the spec describes: point read < 1ms, traversal < 10ms, etc.
func TestCeilingValuesInExpectedRange(t *testing.T) {
	cases := []struct {
		class Class
		max   time.Duration
	}{
		{PointRead, time.Millisecond},
		{Traversal, 10 * time.Millisecond},
		{Subgraph, 100 * time.Millisecond},
		{Write, 10 * time.Millisecond},
	}
	for _, c := range cases {
		ceil := For(c.class)
		if ceil.P99 > c.max {
			t.Errorf("For(%v).P99 = %v, expected < %v", c.class, ceil.P99, c.max)
		}
	}
}

// TestSetGetKnownLabel checks that a known Set label returns its own ceiling.
func TestSetGetKnownLabel(t *testing.T) {
	custom := Ceiling{P99: 42 * time.Millisecond}
	s := Set{
		"custom/SF1": {Traversal: custom},
	}
	table := s.Get("custom/SF1")
	if table[Traversal].P99 != 42*time.Millisecond {
		t.Errorf("Set.Get(known) returned unexpected ceiling: %v", table[Traversal])
	}
}

// TestSetGetFallback proves that an unknown label falls back to the default
// ceilings rather than returning nil or a zero map.
func TestSetGetFallback(t *testing.T) {
	s := Set{}
	table := s.Get("no-such-label")
	if len(table) == 0 {
		t.Error("Set.Get(unknown) returned empty table, expected fallback to defaults")
	}
	if !table[Traversal].Bounded() {
		t.Error("fallback table Traversal should be bounded")
	}
}

// TestDefaultSetHasInprocSF1 proves the shipped DefaultSet contains the
// in-process SF1 entry the smoke gate uses by default.
func TestDefaultSetHasInprocSF1(t *testing.T) {
	table := DefaultSet.Get("inproc/SF1")
	if len(table) == 0 {
		t.Error("DefaultSet[inproc/SF1] is empty")
	}
	if !table[PointRead].Bounded() {
		t.Error("DefaultSet[inproc/SF1] PointRead should be bounded")
	}
}

// TestForInSet checks ForIn returns the ceiling from the named set entry.
func TestForInSet(t *testing.T) {
	custom := Ceiling{P99: 7 * time.Millisecond}
	s := Set{"x": {Traversal: custom}}
	got := ForIn(s, "x", Traversal)
	if got.P99 != 7*time.Millisecond {
		t.Errorf("ForIn(s, x, Traversal) = %v, want 7ms", got.P99)
	}
}
