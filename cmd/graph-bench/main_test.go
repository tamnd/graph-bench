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
	want := []string{"generate", "list", "run", "compare", "report", "gate"}
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

// TestVerbsImplemented confirms the verbs that were previously stubs now return
// real errors (not the "not implemented" placeholder). Each verb is called with
// no arguments so it hits the input-validation path, not the stub path.
func TestVerbsImplemented(t *testing.T) {
	// list with no args must succeed.
	listCmd := newListCmd()
	if err := listCmd.RunE(listCmd, nil); err != nil {
		t.Errorf("list (no args) returned error: %v", err)
	}

	// run with no --workload flag must return a flag-validation error, not "not implemented".
	runCmd := newRunCmd()
	err := runCmd.RunE(runCmd, nil)
	if err == nil {
		t.Error("run with no --workload returned nil, want an error")
	} else if strings.Contains(err.Error(), "not implemented") {
		t.Errorf("run is still a stub: %v", err)
	}

	// report with no inputs returns a "no results" error, not "not implemented".
	reportCmd := newReportCmd()
	err = reportCmd.RunE(reportCmd, nil)
	if err == nil {
		t.Error("report with no inputs returned nil, want an error")
	} else if strings.Contains(err.Error(), "not implemented") {
		t.Errorf("report is still a stub: %v", err)
	}

	// gate with no inputs returns a "no results" error, not "not implemented".
	gateCmd := newGateCmd()
	err = gateCmd.RunE(gateCmd, nil)
	if err == nil {
		t.Error("gate with no inputs returned nil, want an error")
	} else if strings.Contains(err.Error(), "not implemented") {
		t.Errorf("gate is still a stub: %v", err)
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
