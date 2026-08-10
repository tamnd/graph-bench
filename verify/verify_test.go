package verify

import (
	"context"
	"errors"
	"testing"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

// fakeResult replays canned rows.
type fakeResult struct {
	cols []string
	rows [][]engine.Value
	i    int
}

func (r *fakeResult) Columns() []string { return r.cols }
func (r *fakeResult) Next() bool        { r.i++; return r.i <= len(r.rows) }
func (r *fakeResult) Row() []engine.Value {
	return r.rows[r.i-1]
}
func (r *fakeResult) Err() error   { return nil }
func (r *fakeResult) Close() error { return nil }

// fakeSession answers by query text.
type fakeSession struct {
	answers map[string]*fakeResult // keyed by op.Text
	execErr map[string]error
	log     []string
}

func (s *fakeSession) Version(context.Context) (string, error) { return "fake-1", nil }
func (s *fakeSession) Load(context.Context, engine.Dataset) (engine.LoadStats, error) {
	return engine.LoadStats{}, nil
}
func (s *fakeSession) Exec(_ context.Context, op engine.Op) (engine.Result, error) {
	s.log = append(s.log, op.Text)
	if err := s.execErr[op.Text]; err != nil {
		return nil, err
	}
	if r, ok := s.answers[op.Text]; ok {
		cp := *r
		cp.i = 0
		return &cp, nil
	}
	return &fakeResult{cols: []string{"ok"}, rows: [][]engine.Value{{true}}}, nil
}
func (s *fakeSession) Begin(context.Context, engine.AccessMode) (engine.Tx, error) {
	return nil, engine.ErrNoTransactions
}
func (s *fakeSession) Close(context.Context) error { return nil }

func info(dialects ...engine.Dialect) engine.Info {
	return engine.Info{
		Name: "fake", Plane: engine.InProc, Dialects: dialects,
		Caps: engine.Capabilities{Transactions: true, Algorithms: []string{"bfs"}},
	}
}

func refConst(rows [][]engine.Value) *workload.RefStrategy {
	return &workload.RefStrategy{
		Compute: func(engine.Dataset, workload.Params) (*workload.Answer, error) {
			return &workload.Answer{Columns: []string{"n"}, Rows: rows, Unordered: true}, nil
		},
	}
}

func TestRunPassFailSkip(t *testing.T) {
	sess := &fakeSession{
		answers: map[string]*fakeResult{
			"good": {cols: []string{"n"}, rows: [][]engine.Value{{int64(1)}}},
			"bad":  {cols: []string{"n"}, rows: [][]engine.Value{{int64(99)}}},
		},
	}
	w := &workload.Workload{
		Name: "t",
		Queries: []*workload.Query{
			{
				ID: "q-pass", Class: engine.PointRead,
				Texts:     map[engine.Dialect]string{engine.Cypher: "good"},
				Params:    workload.Fixed{P: workload.Params{"id": int64(1)}},
				Reference: refConst([][]engine.Value{{int64(1)}}),
			},
			{
				ID: "q-fail", Class: engine.PointRead,
				Texts:     map[engine.Dialect]string{engine.Cypher: "bad"},
				Params:    workload.Fixed{P: nil},
				Reference: refConst([][]engine.Value{{int64(1)}}),
			},
			{
				ID: "q-skip-dialect", Class: engine.PointRead,
				Texts:     map[engine.Dialect]string{engine.ZuQL: "zonly"},
				Params:    workload.Fixed{P: nil},
				Reference: refConst(nil),
			},
			{
				ID: "q-skip-algo", Class: engine.Analytical, Algorithm: "pagerank",
				Texts:     map[engine.Dialect]string{engine.Cypher: "pr"},
				Params:    workload.Fixed{P: nil},
				Reference: refConst(nil),
			},
		},
	}

	plan, err := Run(context.Background(), sess, info(engine.Cypher), w, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Outcome{
		"q-pass":         Pass,
		"q-fail":         Fail,
		"q-skip-dialect": Skip,
		"q-skip-algo":    Skip,
	}
	for id, o := range want {
		rep, ok := plan.Report(id)
		if !ok {
			t.Fatalf("no report for %s", id)
		}
		if rep.Outcome != o {
			t.Errorf("%s: outcome %s, want %s (reason %q)", id, rep.Outcome, o, rep.Reason)
		}
	}
	if len(plan.Approved) != 1 || plan.Approved[0].Query.ID != "q-pass" {
		t.Errorf("approved = %+v, want only q-pass", plan.Approved)
	}
	if !plan.Failed() {
		t.Error("plan.Failed() = false, want true")
	}
	if rep, _ := plan.Report("q-skip-algo"); rep.Reason != "missing-algorithm:pagerank" {
		t.Errorf("skip reason = %q", rep.Reason)
	}
}

func TestExecErrorIsFail(t *testing.T) {
	sess := &fakeSession{execErr: map[string]error{"boom": errors.New("engine exploded")}}
	w := &workload.Workload{
		Name: "t2",
		Queries: []*workload.Query{{
			ID: "q", Class: engine.PointRead,
			Texts:     map[engine.Dialect]string{engine.Cypher: "boom"},
			Params:    workload.Fixed{P: nil},
			Reference: refConst(nil),
		}},
	}
	plan, err := Run(context.Background(), sess, info(engine.Cypher), w, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	rep, _ := plan.Report("q")
	if rep.Outcome != Fail {
		t.Fatalf("outcome = %s, want FAIL (reason %q)", rep.Outcome, rep.Reason)
	}
}

func TestWritePostConditionAndPoison(t *testing.T) {
	sess := &fakeSession{
		answers: map[string]*fakeResult{
			"check-ok":  {cols: []string{"ok"}, rows: [][]engine.Value{{true}}},
			"check-bad": {cols: []string{"ok"}, rows: [][]engine.Value{{false}}},
		},
		execErr: map[string]error{"teardown-boom": errors.New("nope")},
	}
	wq := func(id, post, teardown string) *workload.Query {
		return &workload.Query{
			ID: id, Class: engine.Write,
			Texts:         map[engine.Dialect]string{engine.Cypher: "write " + id},
			Params:        workload.Fixed{P: nil},
			PostCondition: post,
			Teardown:      teardown,
		}
	}
	w := &workload.Workload{Name: "t3", Queries: []*workload.Query{
		wq("w-ok", "check-ok", ""),
		wq("w-bad", "check-bad", ""),
		wq("w-poison", "check-ok", "teardown-boom"),
		wq("w-after-poison", "check-ok", ""),
	}}

	plan, err := Run(context.Background(), sess, info(engine.Cypher), w, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep, _ := plan.Report("w-ok"); rep.Outcome != Pass {
		t.Errorf("w-ok = %s (%q)", rep.Outcome, rep.Reason)
	}
	if rep, _ := plan.Report("w-bad"); rep.Outcome != Fail {
		t.Errorf("w-bad = %s, want FAIL", rep.Outcome)
	}
	if rep, _ := plan.Report("w-poison"); rep.Outcome != Fail || rep.Reason != "teardown-failed" {
		t.Errorf("w-poison = %s (%q)", rep.Outcome, rep.Reason)
	}
	if !plan.Poisoned {
		t.Error("plan not poisoned")
	}
	if _, ok := plan.Report("w-after-poison"); ok {
		t.Error("verification continued past a poisoned teardown")
	}
}

func TestWriteSkipsWithoutTransactions(t *testing.T) {
	sess := &fakeSession{}
	in := info(engine.Cypher)
	in.Caps.Transactions = false
	w := &workload.Workload{Name: "t4", Queries: []*workload.Query{{
		ID: "w", Class: engine.Write,
		Texts:         map[engine.Dialect]string{engine.Cypher: "write"},
		Params:        workload.Fixed{P: nil},
		PostCondition: "check",
	}}}
	plan, err := Run(context.Background(), sess, in, w, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep, _ := plan.Report("w"); rep.Outcome != Skip || rep.Reason != "no-transactions" {
		t.Errorf("got %s (%q), want SKIP no-transactions", rep.Outcome, rep.Reason)
	}
}
