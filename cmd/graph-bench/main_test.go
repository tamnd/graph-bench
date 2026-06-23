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

// implementedVerbs are the verbs wired to real work; the rest are still stubs.
// generate landed with the dataset milestone.
var implementedVerbs = map[string]bool{"generate": true}

// TestVerbsAreStubs confirms the not-yet-implemented verbs report clearly rather
// than pretending to succeed, which would hide that no work happened. Verbs that
// have been implemented are exempt and checked elsewhere.
func TestVerbsAreStubs(t *testing.T) {
	root := newRootCmd()
	for _, c := range root.Commands() {
		if c.RunE == nil || implementedVerbs[c.Name()] {
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

// TestGenerateRequiresGen confirms the generate verb is wired (not a stub) and
// reports the missing required flag rather than silently doing nothing.
func TestGenerateRequiresGen(t *testing.T) {
	cmd := newGenerateCmd()
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("generate with no --gen returned nil, want a required-flag error")
	}
	if strings.Contains(err.Error(), "not implemented") {
		t.Errorf("generate is still a stub: %v", err)
	}
	if !strings.Contains(err.Error(), "--gen") {
		t.Errorf("generate error = %q, want it to mention --gen", err)
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
