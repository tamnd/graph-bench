package main

import (
	"context"
	"errors"
	"testing"

	"github.com/tamnd/graph-bench/engine"
)

// flakyEngine fails the first fails calls to Start and succeeds after, which
// is what a server that has bound its port but not yet enabled its protocol
// looks like from out here.
type flakyEngine struct {
	fails int
	calls int
}

func (e *flakyEngine) Info() engine.Info { return engine.Info{Name: "flaky"} }

func (e *flakyEngine) Start(context.Context, engine.Config) (engine.Session, error) {
	e.calls++
	if e.calls <= e.fails {
		return nil, errors.New("EOF")
	}
	return stubSession{}, nil
}

type stubSession struct{}

func (stubSession) Version(context.Context) (string, error) { return "0", nil }
func (stubSession) Load(context.Context, engine.Dataset) (engine.LoadStats, error) {
	return engine.LoadStats{}, nil
}
func (stubSession) Exec(context.Context, engine.Op) (engine.Result, error) { return nil, nil }
func (stubSession) Begin(context.Context, engine.AccessMode) (engine.Tx, error) {
	return nil, engine.ErrNoTransactions
}
func (stubSession) Close(context.Context) error { return nil }

// TestStartSessionRetriesManagedServer proves a server this run launched is
// given time to come up. A container accepts a connection as soon as it binds
// the port, which for Neo4j is many seconds before Bolt answers on it, and
// the first attempt used to end the run reporting an engine that is not there.
func TestStartSessionRetriesManagedServer(t *testing.T) {
	eng := &flakyEngine{fails: 2}
	sess, err := startSession(context.Background(), eng, engine.Config{}, true)
	if err != nil {
		t.Fatalf("startSession: %v", err)
	}
	if sess == nil {
		t.Fatal("startSession returned no session")
	}
	if eng.calls != 3 {
		t.Errorf("Start called %d times, want 3", eng.calls)
	}
}

// TestStartSessionDoesNotRetrySuppliedServer proves a server the operator
// pointed the run at gets one attempt. That one is either up or not, and
// waiting a minute and a half to say so helps nobody.
func TestStartSessionDoesNotRetrySuppliedServer(t *testing.T) {
	eng := &flakyEngine{fails: 1}
	if _, err := startSession(context.Background(), eng, engine.Config{}, false); err == nil {
		t.Fatal("startSession succeeded, want the first failure reported")
	}
	if eng.calls != 1 {
		t.Errorf("Start called %d times, want 1", eng.calls)
	}
}

// TestStartSessionStopsWhenCancelled proves a cancelled run stops retrying
// rather than holding the terminal for the whole window.
func TestStartSessionStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	eng := &flakyEngine{fails: 100}
	if _, err := startSession(ctx, eng, engine.Config{}, true); err == nil {
		t.Fatal("startSession succeeded on a cancelled context")
	}
	if eng.calls != 1 {
		t.Errorf("Start called %d times, want 1", eng.calls)
	}
}
