package main

import (
	"strings"
	"testing"
)

// TestRootCommandTree checks that the command tree builds and carries every
// verb the spec names, so a missing or renamed verb fails the smoke gate.
func TestRootCommandTree(t *testing.T) {
	root := newRootCmd()
	if root.Use != "graph-bench" {
		t.Fatalf("root Use = %q, want graph-bench", root.Use)
	}
	want := []string{"generate", "run", "compare", "report", "gate"}
	have := map[string]bool{}
	for _, c := range root.Commands() {
		have[c.Name()] = true
	}
	for _, v := range want {
		if !have[v] {
			t.Errorf("missing verb %q", v)
		}
	}
}

// TestVerbsAreStubs confirms the unimplemented verbs report clearly rather than
// pretending to succeed, which would hide that no work happened.
func TestVerbsAreStubs(t *testing.T) {
	root := newRootCmd()
	for _, c := range root.Commands() {
		if c.RunE == nil {
			continue
		}
		err := c.RunE(c, nil)
		if err == nil {
			t.Errorf("verb %q returned nil, want a not-implemented error", c.Name())
			continue
		}
		if !strings.Contains(err.Error(), "not implemented") {
			t.Errorf("verb %q error = %q, want it to say not implemented", c.Name(), err)
		}
	}
}

// TestExitCode maps the documented cases: nil is zero, a plain error is one.
func TestExitCode(t *testing.T) {
	if got := exitCode(nil); got != 0 {
		t.Errorf("exitCode(nil) = %d, want 0", got)
	}
	if got := exitCode(errStub); got != 1 {
		t.Errorf("exitCode(err) = %d, want 1", got)
	}
}

var errStub = stubErr("boom")

type stubErr string

func (e stubErr) Error() string { return string(e) }
