// Package verify is the toll gate between load and measurement. The
// runner's phase order is load → verify → warmup → measure, and the
// measure phase takes the verified plan as its input type, so an
// unverified query cannot be timed by construction (spec 08 §5).
package verify

import (
	"context"
	"fmt"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

// Outcome is a per-query verification verdict.
type Outcome string

const (
	// Pass means the engine's answers matched the reference; the query
	// is cleared for timing.
	Pass Outcome = "PASS"
	// Fail means an answer mismatch or an execution error on a query the
	// engine claimed dialect support for. A wrong engine is news (F6):
	// the engine's run aborts with the diff in the report.
	Fail Outcome = "FAIL"
	// Skip means the query never reached the engine: no dialect text,
	// missing capability, or probe failure. Recorded with a
	// machine-readable reason; a SKIP is respectable, a fake PASS is not.
	Skip Outcome = "SKIP"
)

// QueryReport is the verdict for one query against one engine.
type QueryReport struct {
	QueryID string
	Outcome Outcome

	// Reason is machine-readable: "no-dialect-text",
	// "missing-algorithm:<name>", "no-transactions", "exec-error: …",
	// or the first mismatch diff for FAIL.
	Reason string

	// Dialect and Text are the resolved statement, empty on SKIP.
	Dialect engine.Dialect
	Text    string

	// Samples is how many parameter draws were checked.
	Samples int
}

// Approved is one query cleared for timing, with its resolved text.
type Approved struct {
	Query   *workload.Query
	Dialect engine.Dialect
	Text    string
}

// Plan is the verified execution plan the measure phase consumes.
type Plan struct {
	Workload *workload.Workload
	Engine   engine.Info

	// Approved queries, in workload order.
	Approved []Approved

	// Reports covers every query, including skips and failures.
	Reports []QueryReport

	// Sampled marks validation on a parameter subset (validation:sampled
	// in the condition stamp) because the run exceeds ValidationScale.
	Sampled bool

	// Poisoned marks a failed write teardown: stationarity is gone and
	// the run must not be measured.
	Poisoned bool
}

// Failed reports whether any query failed verification.
func (p *Plan) Failed() bool {
	for _, r := range p.Reports {
		if r.Outcome == Fail {
			return true
		}
	}
	return false
}

// Report returns the report for a query ID.
func (p *Plan) Report(id string) (QueryReport, bool) {
	for _, r := range p.Reports {
		if r.QueryID == id {
			return r, true
		}
	}
	return QueryReport{}, false
}

// Options tunes a verification run.
type Options struct {
	// SampleDefault caps validation draws per query when the query's
	// reference does not declare its own SampleSize. 0 means 4.
	SampleDefault int

	// Sampled marks the run as exceeding the workload's ValidationScale;
	// it is recorded into the plan and the condition stamp.
	Sampled bool
}

func (o Options) sampleDefault() int {
	if o.SampleDefault <= 0 {
		return 4
	}
	return o.SampleDefault
}

// Run verifies every query in the workload against the engine and
// returns the plan. It never returns early on a FAIL — the full report
// is the deliverable — but a poisoned teardown stops further writes.
func Run(ctx context.Context, sess engine.Session, info engine.Info, w *workload.Workload, ds engine.Dataset, opts Options) (*Plan, error) {
	plan := &Plan{Workload: w, Engine: info, Sampled: opts.Sampled}
	for _, q := range w.Queries {
		rep := verifyQuery(ctx, sess, info, q, ds, opts)
		plan.Reports = append(plan.Reports, rep)
		switch rep.Outcome {
		case Pass:
			plan.Approved = append(plan.Approved, Approved{Query: q, Dialect: rep.Dialect, Text: rep.Text})
		case Fail:
			if q.Class == engine.Write && rep.Reason == poisonedReason {
				plan.Poisoned = true
			}
		}
		if plan.Poisoned {
			break
		}
		if err := ctx.Err(); err != nil {
			return plan, err
		}
	}
	return plan, nil
}

const poisonedReason = "teardown-failed"

func verifyQuery(ctx context.Context, sess engine.Session, info engine.Info, q *workload.Query, ds engine.Dataset, opts Options) QueryReport {
	rep := QueryReport{QueryID: q.ID}

	if q.NeedsPathPredicate && !info.Caps.PathPredicates {
		rep.Outcome = Skip
		rep.Reason = "no-path-predicates"
		return rep
	}
	if q.NeedsShortestPath && !info.Caps.ShortestPaths {
		rep.Outcome = Skip
		rep.Reason = "no-shortest-paths"
		return rep
	}
	if q.Class == engine.Write && !info.Caps.Transactions && !q.AutocommitOK {
		rep.Outcome = Skip
		rep.Reason = "no-transactions"
		return rep
	}

	// Text first, capability second. An engine that has a text for an
	// analytical query can answer it, whether or not it also ships a
	// kernel under that name: Kùzu has no bfs procedure and still
	// answers ga-bfs out of a shortest-path expansion. The kernel
	// capability is only the reason when there is no text to run, where
	// it is the more precise half of "no-dialect-text".
	dialect, text, ok := engine.ResolveText(info.Dialects, q.Texts)
	if !ok {
		rep.Outcome = Skip
		rep.Reason = "no-dialect-text"
		if q.Algorithm != "" && !info.Caps.HasAlgorithm(q.Algorithm) {
			rep.Reason = "missing-algorithm:" + q.Algorithm
		}
		return rep
	}
	rep.Dialect, rep.Text = dialect, text

	if q.Class == engine.Write {
		return verifyWrite(ctx, sess, q, ds, rep)
	}
	return verifyRead(ctx, sess, q, ds, opts, rep)
}

func verifyRead(ctx context.Context, sess engine.Session, q *workload.Query, ds engine.Dataset, opts Options, rep QueryReport) QueryReport {
	samples := opts.sampleDefault()
	if q.Reference.SampleSize > 0 {
		samples = q.Reference.SampleSize
	}
	pool := q.Params.Pool()
	if len(pool) > 0 && len(pool) < samples {
		samples = len(pool)
	}
	if len(pool) == 0 {
		samples = 1 // fixed params: one draw is the whole space
	}

	for i := 0; i < samples; i++ {
		params := q.Params.Next()
		got, err := execToAnswer(ctx, sess, engine.Op{
			QueryID: q.ID, Class: q.Class, Dialect: rep.Dialect,
			Text: rep.Text, Params: params,
		})
		if err != nil {
			rep.Outcome = Fail
			rep.Reason = fmt.Sprintf("exec-error: %v", err)
			return rep
		}
		want, err := q.Reference.Compute(ds, params)
		if err != nil {
			rep.Outcome = Fail
			rep.Reason = fmt.Sprintf("oracle-error: %v", err)
			return rep
		}
		if err := workload.Compare(got, want, q.Reference.Compare); err != nil {
			rep.Outcome = Fail
			rep.Reason = fmt.Sprintf("sample %d %v: %v", i, params, err)
			return rep
		}
		rep.Samples++
	}
	rep.Outcome = Pass
	return rep
}

// verifyWrite checks a write query's effect once: setup, execute, post-
// condition, teardown. The post-condition contract: its query returns
// exactly one row whose first column is boolean true. A failed teardown
// poisons the run (stationarity is gone).
func verifyWrite(ctx context.Context, sess engine.Session, q *workload.Query, ds engine.Dataset, rep QueryReport) QueryReport {
	run := func(text string) error {
		res, err := sess.Exec(ctx, engine.Op{QueryID: q.ID, Class: engine.Write, Dialect: rep.Dialect, Text: text})
		if err != nil {
			return err
		}
		for res.Next() {
		}
		if err := res.Err(); err != nil {
			res.Close()
			return err
		}
		return res.Close()
	}

	if q.Setup != "" {
		if err := run(q.Setup); err != nil {
			rep.Outcome = Fail
			rep.Reason = fmt.Sprintf("setup-error: %v", err)
			return rep
		}
	}

	params := q.Params.Next()
	if _, err := execToAnswer(ctx, sess, engine.Op{
		QueryID: q.ID, Class: engine.Write, Dialect: rep.Dialect,
		Text: rep.Text, Params: params,
	}); err != nil {
		rep.Outcome = Fail
		rep.Reason = fmt.Sprintf("exec-error: %v", err)
		// Still attempt teardown below: a failed write must not leave
		// residue for the next query.
	} else if check := q.Check(rep.Dialect); check != "" {
		got, err := execToAnswer(ctx, sess, engine.Op{
			QueryID: q.ID, Class: engine.PointRead, Dialect: rep.Dialect,
			Text: check, Params: params,
		})
		switch {
		case err != nil:
			rep.Outcome = Fail
			rep.Reason = fmt.Sprintf("post-condition-error: %v", err)
		case len(got.Rows) != 1 || len(got.Rows[0]) == 0 || got.Rows[0][0] != true:
			rep.Outcome = Fail
			rep.Reason = fmt.Sprintf("post-condition not satisfied: got %v", got.Rows)
		default:
			rep.Outcome = Pass
			rep.Samples = 1
		}
	} else if q.Reference != nil {
		// Reference-checked write: compare the write's own result rows.
		want, oerr := q.Reference.Compute(ds, params)
		if oerr != nil {
			rep.Outcome = Fail
			rep.Reason = fmt.Sprintf("oracle-error: %v", oerr)
		} else if got, err := execToAnswer(ctx, sess, engine.Op{
			QueryID: q.ID, Class: engine.PointRead, Dialect: rep.Dialect,
			Text: rep.Text, Params: params,
		}); err != nil {
			rep.Outcome = Fail
			rep.Reason = fmt.Sprintf("exec-error: %v", err)
		} else if cerr := workload.Compare(got, want, q.Reference.Compare); cerr != nil {
			rep.Outcome = Fail
			rep.Reason = cerr.Error()
		} else {
			rep.Outcome = Pass
			rep.Samples = 1
		}
	} else {
		rep.Outcome = Pass
		rep.Samples = 1
	}

	if q.Teardown != "" {
		if err := run(q.Teardown); err != nil {
			rep.Outcome = Fail
			rep.Reason = poisonedReason
		}
	}
	return rep
}

// execToAnswer runs one op and drains its result into an Answer.
func execToAnswer(ctx context.Context, sess engine.Session, op engine.Op) (*workload.Answer, error) {
	res, err := sess.Exec(ctx, op)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	ans := &workload.Answer{Columns: res.Columns()}
	for res.Next() {
		row := res.Row()
		cp := make([]engine.Value, len(row))
		copy(cp, row)
		ans.Rows = append(ans.Rows, cp)
	}
	if err := res.Err(); err != nil {
		return nil, err
	}
	return ans, nil
}
