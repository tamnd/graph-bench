// The run pipeline, per engine: resolve dataset -> Start -> Load -> bind
// parameter pools -> verify (the toll gate, printed before any timing) ->
// warmup -> measure -> stamp Condition -> Document. Ported from v1's exec.go
// and adapted to the v0.3 phase order (spec 08 §5, 09 §1).
package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/measure"
	"github.com/tamnd/graph-bench/report"
	"github.com/tamnd/graph-bench/setup"
	"github.com/tamnd/graph-bench/verify"
	"github.com/tamnd/graph-bench/workload"
)

// runConfig carries the run verb's resolved flags into the pipeline.
type runConfig struct {
	profile     profile
	scale       string // "smoke" or "sf1" (condition stamp + recipe mapping)
	count       int    // per-query count override; 0 = profile default
	rate        float64
	concurrency int
	outDir      string
	publish     bool
	tuned       bool
	datasetsDir string
	seed        int64
	noDocker    bool
	stderr      io.Writer
	stdout      io.Writer
}

// executeRun runs one workload against one engine and returns the result
// document. A verification FAIL is not an error: the document records it and
// measurement is skipped (a wrong engine is news, F6). An error means the
// harness could not complete the phases.
func executeRun(ctx context.Context, engName string, wl *workload.Workload, rc runConfig) (*report.Document, error) {
	eng, err := engine.Lookup(engName)
	if err != nil {
		return nil, err
	}
	info := eng.Info()

	ds, err := resolveDataset(ctx, wl.Dataset, rc.scale, rc.datasetsDir)
	if err != nil {
		return nil, fmt.Errorf("dataset: %w", err)
	}

	// Engine config. Embedded and subprocess engines get a pinned database
	// path so a cold-pass restart reopens the same files; Bolt engines get a
	// managed container when no server is configured (port of v1 behavior).
	cfg := engine.Config{Values: map[string]string{}, Tuned: rc.tuned}
	var dbTempDir string
	if info.Plane == engine.Subprocess || info.Plane == engine.InProc {
		tmp, err := os.MkdirTemp("", "graph-bench-db-*")
		if err != nil {
			return nil, fmt.Errorf("%s: temp db dir: %w", engName, err)
		}
		dbTempDir = tmp
		cfg.Values["path"] = filepath.Join(tmp, "bench.zu1")
	}
	defer func() {
		if dbTempDir != "" {
			os.RemoveAll(dbTempDir)
		}
	}()
	container, err := startContainerIfNeeded(ctx, engName, rc)
	if err != nil {
		return nil, err
	}
	if container != nil {
		cfg.Values["uri"] = container.BoltURI
		defer container.Stop(context.WithoutCancel(ctx))
	}

	// Start and load.
	usageStart := measure.Snapshot()
	sess, err := eng.Start(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("%s: Start: %w", engName, err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = sess.Close(context.WithoutCancel(ctx))
		}
	}()

	// Re-read Info now that a session exists. Some capabilities cannot be
	// known before Start — whether transactions work, which graph kernels
	// the build actually exposes — so an adapter probes them there and
	// refines what Info reports. Reading Info only once, before Start,
	// pinned every such capability to its pre-probe default and SKIPped
	// every analytical query on an engine that in fact has the kernels.
	info = eng.Info()

	engVersion, _ := sess.Version(ctx)
	loadStats, err := sess.Load(ctx, ds)
	if err != nil {
		return nil, fmt.Errorf("%s: Load: %w", engName, err)
	}

	// Bind curated parameter pools (queries with a PoolKey and no Params).
	unbound := bindParams(wl, ds, rc.seed)

	// Pre-filter: capability and dialect skips are verify's own job, so all
	// this drops is a query whose parameter pool never bound.
	vw, preskips := prefilterWorkload(wl, unbound)

	// Verify — the toll gate, printed before any timing output (spec 09 §1).
	sampled := wl.ValidationScale != "" && rc.scale != "smoke" && wl.ValidationScale != rc.scale
	plan, err := verify.Run(ctx, sess, info, vw, ds, verify.Options{
		SampleDefault: rc.profile.sampleDefault,
		Sampled:       sampled,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: verify: %w", engName, err)
	}
	plan.Reports = append(preskips, plan.Reports...)
	printVerification(rc.stdout, engName, plan.Reports)

	startedAt := time.Now().UTC()
	var res measure.Result
	measured := false
	if plan.Failed() || plan.Poisoned {
		fmt.Fprintf(rc.stderr, "run: %s: verification failed; measurement skipped\n", engName)
	} else if len(plan.Approved) == 0 {
		fmt.Fprintf(rc.stderr, "run: %s: no queries approved (all SKIP); nothing to measure\n", engName)
	} else {
		res, err = measurePlan(ctx, sess, eng, cfg, ds, wl, plan, rc, &closed)
		if err != nil {
			return nil, fmt.Errorf("%s: measure: %w", engName, err)
		}
		measured = true
	}
	finishedAt := time.Now().UTC()

	res.Load = loadStats
	res.Condition = buildCondition(ctx, info, sess, cfg, wl, ds, plan, rc, engVersion, res, measured, startedAt, finishedAt)

	// Close the session before the closing reading, and take the store size
	// after the close. A subprocess engine's CPU and peak resident set only
	// reach the children rusage once the child has been waited for, and a store
	// only holds every byte the run made durable once the engine has shut down,
	// so a reading taken with the engine still up reports a subprocess engine
	// as free and its store as smaller than it is. buildCondition runs first
	// because it probes the session for the stamp fields.
	if !closed {
		_ = sess.Close(context.WithoutCancel(ctx))
		closed = true
	}
	res.Resource = measure.CaptureResource(usageStart, measure.Snapshot(), measure.Disk{
		DatasetBytes: measure.DirSizeBytes(ds.Dir()),
		LoadBytes:    loadStats.BytesOnDisk,
		StoreBytes:   measure.DirSizeBytes(dbTempDir),
	})

	doc := report.FromMeasure(wl.Name, wl.Family, wl.Fidelity, res, toVerifications(plan.Reports))
	return doc, nil
}

// measurePlan runs the measurement phase appropriate to the workload shape:
// analytics repetitions, mixed schedule, or per-query service time. closed is
// set when a cold pass replaced the session (the replacement is closed here).
func measurePlan(
	ctx context.Context,
	sess engine.Session,
	eng engine.Engine,
	cfg engine.Config,
	ds engine.Dataset,
	wl *workload.Workload,
	plan *verify.Plan,
	rc runConfig,
	closed *bool,
) (measure.Result, error) {
	prof := rc.profile
	info := eng.Info()

	// Analytics workloads: single-stream repetitions, no concurrency (08 §4).
	if wl.Analytics {
		ops := make([]engine.Op, 0, len(plan.Approved))
		for _, a := range plan.Approved {
			ops = append(ops, approvedOp(a))
		}
		ar, err := measure.RunAnalytics(ctx, sess, ops, prof.analyticsReps, prof.discardFirst)
		if err != nil {
			return measure.Result{}, err
		}
		return analyticsResult(ar, plan), nil
	}

	// Mixed workloads: weighted deterministic interleave (BuildMixedSchedule).
	if wl.Mix != nil {
		perQuery := map[string][]engine.Op{}
		for _, a := range plan.Approved {
			perQuery[a.Query.ID] = buildOpsFor(a, 64)
		}
		total := prof.mixCount
		if rc.count > 0 {
			total = rc.count
		}
		var warmupDur time.Duration
		if rc.rate > 0 {
			// Open model: warmup is a schedule prefix (offset < Warmup).
			warmupDur = warmupWindow(prof)
			total += int(rc.rate * warmupDur.Seconds())
		}
		ops := measure.BuildMixedSchedule(perQuery, wl.Mix.Weights, rc.seed, total, rc.rate, warmupDur)
		brackets := bracketsFor(plan)
		opt := measure.Options{
			Rate:        rc.rate,
			Count:       total,
			Warmup:      warmupDur,
			Concurrency: rc.concurrency,
			Brackets:    brackets,
		}
		if rc.rate <= 0 {
			warmupPass(ctx, sess, sampleOps(perQuery), prof, brackets)
		}
		res := measure.Run(ctx, sess, ops, opt)
		if prof.sweep {
			sw := measure.Sweep(ctx, sess, ops, opt, measure.CISweepPoints)
			res.Sweep = sw.Sweep
		}
		return res, nil
	}

	// Plain workloads: per-query service-time in count mode. One Run over the
	// concatenation: Result.ByQuery carries per-query stats, Stats the rollup.
	var ops []measure.Op
	for _, a := range plan.Approved {
		n := rc.count
		if n <= 0 {
			n = prof.countFor(a.Query.Class)
		}
		for _, op := range buildOpsFor(a, n) {
			ops = append(ops, measure.Op{Op: op})
		}
	}
	brackets := bracketsFor(plan)
	warmupPass(ctx, sess, firstOps(plan), prof, brackets)
	res := measure.Run(ctx, sess, ops, measure.Options{
		Count:       len(ops),
		Concurrency: rc.concurrency,
		Budget:      prof.budget,
		Brackets:    brackets,
	})

	// Cold pass: persistent engines only, full profile. Restart the session
	// and drop the page cache so the first access is genuinely cold (08 §4).
	if prof.cold && info.Caps.Persistent {
		if err := sess.Close(ctx); err == nil {
			setup.DropCaches()
			cold, err := eng.Start(ctx, cfg)
			if err != nil {
				return res, fmt.Errorf("cold restart: %w", err)
			}
			defer func() {
				_ = cold.Close(context.WithoutCancel(ctx))
			}()
			*closed = true
			coldRes := measure.ColdRun(ctx, cold, firstMeasureOps(plan), 0)
			res = measure.MergeCold(res, coldRes)
		}
	}
	return res, nil
}

// approvedOp resolves one Approved into an engine.Op with the next parameter
// draw from the query's source.
func approvedOp(a verify.Approved) engine.Op {
	op := engine.Op{
		QueryID: a.Query.ID,
		Class:   a.Query.Class,
		Dialect: a.Dialect,
		Text:    a.Text,
	}
	if a.Query.Params != nil {
		op.Params = a.Query.Params.Next()
	}
	return op
}

// buildOpsFor builds n ops for one approved query, drawing parameters in
// deterministic cycle order.
func buildOpsFor(a verify.Approved, n int) []engine.Op {
	ops := make([]engine.Op, 0, n)
	for i := 0; i < n; i++ {
		ops = append(ops, approvedOp(a))
	}
	return ops
}

// bracketsFor collects the untimed Setup/Teardown pair of every approved
// write query, keyed by id, so measure.Run can restore the pre-write state
// between repetitions (spec 06 §0). Queries that name neither statement are
// left out: an entry in the map costs a mutex and a map lookup per op, and a
// read has nothing to restore.
func bracketsFor(plan *verify.Plan) map[string]measure.Bracket {
	br := map[string]measure.Bracket{}
	for _, a := range plan.Approved {
		setup, teardown := a.Query.Before(a.Dialect), a.Query.After(a.Dialect)
		if setup == "" && teardown == "" {
			continue
		}
		br[a.Query.ID] = measure.Bracket{Setup: setup, Teardown: teardown}
	}
	if len(br) == 0 {
		return nil
	}
	return br
}

// firstOps returns one op per approved query (used to exercise every query
// during warmup).
func firstOps(plan *verify.Plan) []engine.Op {
	ops := make([]engine.Op, 0, len(plan.Approved))
	for _, a := range plan.Approved {
		ops = append(ops, approvedOp(a))
	}
	return ops
}

// firstMeasureOps is firstOps wrapped for the cold pass: one distinct op per
// query so the cold map carries one first-access latency per query.
func firstMeasureOps(plan *verify.Plan) []measure.Op {
	var ops []measure.Op
	for _, op := range firstOps(plan) {
		ops = append(ops, measure.Op{Op: op})
	}
	return ops
}

// sampleOps flattens one op per query from a perQuery pool map, in sorted id
// order for determinism.
func sampleOps(perQuery map[string][]engine.Op) []engine.Op {
	ids := make([]string, 0, len(perQuery))
	for id := range perQuery {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var ops []engine.Op
	for _, id := range ids {
		if pool := perQuery[id]; len(pool) > 0 {
			ops = append(ops, pool[0])
		}
	}
	return ops
}

// warmupWindow is the warmup duration for open-model schedules: the profile's
// fixed window, or the WarmupConfig fixed floor (3s, spec 08 §3).
func warmupWindow(prof profile) time.Duration {
	if prof.fixedWarmup > 0 {
		return prof.fixedWarmup
	}
	return 3 * time.Second
}

// warmupPass fires ops unrecorded before a count-mode measurement. The fast
// profile skips the stabilization detector and uses a fixed 2s window; the
// full profile uses the WarmupConfig fixed-fraction rule with its 3s/200-op
// floors (spec 08 §3, fixed path — CI-reproducible).
func warmupPass(ctx context.Context, sess engine.Session, ops []engine.Op, prof profile, brackets map[string]measure.Bracket) {
	if len(ops) == 0 {
		return
	}
	deadline := time.Now().Add(prof.fixedWarmup)
	minOps := 0
	if prof.fixedWarmup == 0 {
		cfg := measure.WarmupConfig{}
		minOps = cfg.WarmupOps(200 * len(ops))
		deadline = time.Now().Add(3 * time.Second)
	}
	fired := 0
	for i := 0; ; i++ {
		if ctx.Err() != nil {
			return
		}
		if time.Now().After(deadline) && fired >= minOps {
			return
		}
		op := ops[i%len(ops)]
		qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		// Warmup repeats a write as many times as the measured run does, so it
		// needs the same bracket: without it the warmup pass is what leaves the
		// graph in the state the measured repetitions then fail against.
		measure.Fire(qctx, sess, op, brackets[op.QueryID])
		cancel()
		fired++
		if fired > 100000 { // safety valve: never warm forever
			return
		}
	}
}

// analyticsResult converts the analytics protocol outcome into a measure
// Result: per-query stats verbatim, per-class stats recomputed from the kept
// repetition durations.
func analyticsResult(ar measure.AnalyticsResult, plan *verify.Plan) measure.Result {
	classOf := map[string]engine.Class{}
	for _, a := range plan.Approved {
		classOf[a.Query.ID] = a.Query.Class
	}
	byClass := map[engine.Class][]time.Duration{}
	for id, durs := range ar.PerQuery {
		cl := classOf[id]
		byClass[cl] = append(byClass[cl], durs...)
	}
	stats := map[engine.Class]measure.Stat{}
	for cl, durs := range byClass {
		s := statFromDurations(durs)
		s.Class = cl
		stats[cl] = s
	}
	return measure.Result{
		Stats:   stats,
		ByQuery: ar.Stats,
		Latency: measure.ServiceTimeLatency,
	}
}

// statFromDurations summarizes a duration slice with nearest-rank
// percentiles, matching the measure package's convention.
func statFromDurations(durs []time.Duration) measure.Stat {
	if len(durs) == 0 {
		return measure.Stat{}
	}
	sorted := append([]time.Duration(nil), durs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := func(p float64) time.Duration {
		i := int(p*float64(len(sorted))+0.999999) - 1
		if i < 0 {
			i = 0
		}
		if i >= len(sorted) {
			i = len(sorted) - 1
		}
		return sorted[i]
	}
	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}
	mean := sum / time.Duration(len(sorted))
	var varSum float64
	for _, d := range sorted {
		diff := float64(d - mean)
		varSum += diff * diff
	}
	stddev := time.Duration(math.Sqrt(varSum / float64(len(sorted))))
	return measure.Stat{
		Count:  len(sorted),
		Min:    sorted[0],
		P50:    rank(0.50),
		P90:    rank(0.90),
		P95:    rank(0.95),
		P99:    rank(0.99),
		Max:    sorted[len(sorted)-1],
		Mean:   mean,
		StdDev: stddev,
	}
}

// bindParams binds curated parameter pools to every query that declares a
// PoolKey and has no Params source yet. A family that ships its own binder
// gets first refusal, because only it knows what makes its parameters
// non-degenerate; whatever it leaves unbound falls to the dataset's
// params.json, and anything still missing is curated on demand (and
// written beside the manifest). Returns the IDs of queries left without a
// source (they SKIP, reason "no-param-pool").
func bindParams(wl *workload.Workload, ds engine.Dataset, seed int64) map[string]bool {
	var missing []string
	if _, err := bindFamilyPools(wl, ds); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s: family pool binding failed: %v\n", wl.Name, err)
	}
	for _, q := range wl.Queries {
		if q.PoolKey == "" || q.Params != nil {
			continue
		}
		pool, err := ds.Params(q.PoolKey)
		if err == nil && len(pool) > 0 {
			q.Params = workload.NewPoolSource(pool)
			continue
		}
		missing = append(missing, q.PoolKey)
	}
	if len(missing) > 0 && ds.Dir() != "" {
		if err := curatePools(ds, missing, curatePoolSize, seed); err == nil {
			for _, q := range wl.Queries {
				if q.PoolKey == "" || q.Params != nil {
					continue
				}
				if pool, err := ds.Params(q.PoolKey); err == nil && len(pool) > 0 {
					q.Params = workload.NewPoolSource(pool)
				}
			}
		}
	}
	unbound := map[string]bool{}
	for _, q := range wl.Queries {
		if q.PoolKey != "" && q.Params == nil {
			unbound[q.ID] = true
		}
	}
	return unbound
}

// prefilterWorkload returns a shallow workload copy restricted to the queries
// the session can execute, plus synthetic SKIP reports for the rest. One
// filter applies here, since capability and dialect skips are verify's job:
// queries with no bound parameter pool.
func prefilterWorkload(wl *workload.Workload, unbound map[string]bool) (*workload.Workload, []verify.QueryReport) {
	allowed := func(q *workload.Query) (bool, string) {
		if unbound[q.ID] {
			return false, "no-param-pool"
		}
		return true, ""
	}

	copyWl := *wl
	copyWl.Queries = nil
	var skips []verify.QueryReport
	for _, q := range wl.Queries {
		if ok, reason := allowed(q); !ok {
			skips = append(skips, verify.QueryReport{QueryID: q.ID, Outcome: verify.Skip, Reason: reason})
			continue
		}
		copyWl.Queries = append(copyWl.Queries, q)
	}
	return &copyWl, skips
}

// printVerification renders the verification table before any timing output
// (spec 09 §1).
func printVerification(w io.Writer, engName string, reports []verify.QueryReport) {
	fmt.Fprintf(w, "verification (%s):\n", engName)
	fmt.Fprintf(w, "  %-20s  %-6s  %-8s  %s\n", "query", "result", "dialect", "reason")
	for _, r := range reports {
		fmt.Fprintf(w, "  %-20s  %-6s  %-8s  %s\n", r.QueryID, r.Outcome, r.Dialect, r.Reason)
	}
}

// toVerifications maps verify reports into the document's plain-data form.
func toVerifications(reports []verify.QueryReport) []report.Verification {
	out := make([]report.Verification, 0, len(reports))
	for _, r := range reports {
		out = append(out, report.Verification{
			QueryID: r.QueryID,
			Outcome: string(r.Outcome),
			Reason:  r.Reason,
			Samples: r.Samples,
			Dialect: string(r.Dialect),
		})
	}
	return out
}

// buildCondition assembles the full Condition stamp (spec 08 §7). Every field
// is filled here; the doc's rule is that no field is zero-valued after a real
// run.
func buildCondition(
	ctx context.Context,
	info engine.Info,
	sess engine.Session,
	cfg engine.Config,
	wl *workload.Workload,
	ds engine.Dataset,
	plan *verify.Plan,
	rc runConfig,
	engVersion string,
	res measure.Result,
	measured bool,
	startedAt, finishedAt time.Time,
) measure.Condition {
	dialects := map[string]string{}
	for _, a := range plan.Approved {
		dialects[a.Query.ID] = string(a.Dialect)
	}
	cfgMap := map[string]string{}
	for k, v := range cfg.Values {
		cfgMap[k] = v
	}
	// zu extras: exec surface, discovered binary, calibrated spawn floor.
	// The floor is a stamp field, never a subtraction (spec 08 §8); on the
	// in-process plane it is zero, which is the point of stamping it.
	if zs, ok := sess.(interface{ Mode() string }); ok {
		cfgMap["zu_mode"] = zs.Mode()
	}
	if zb, ok := sess.(interface{ Bin() string }); ok {
		cfgMap["zu_bin"] = zb.Bin()
	}
	if zc, ok := sess.(interface {
		Calibrate(context.Context) time.Duration
	}); ok && measured {
		cfgMap["zu_spawn_floor"] = zc.Calibrate(ctx).String()
	}

	warmupOutcome := "fixed-fraction"
	if rc.profile.fixedWarmup > 0 {
		warmupOutcome = "fixed-" + rc.profile.fixedWarmup.String()
	}
	validation := "full"
	if plan.Sampled {
		validation = "sampled"
	}
	cache := "warm"
	coldProtocol := "none"
	if len(res.Cold) > 0 {
		cache = "cold"
		if runtime.GOOS == "darwin" {
			coldProtocol = "purge"
		} else {
			coldProtocol = "drop_caches"
		}
	}
	conc := []int{rc.concurrency}
	if len(res.Sweep) > 0 {
		conc = append([]int(nil), measure.CISweepPoints...)
	}
	reps := 0
	if wl.Analytics {
		reps = rc.profile.analyticsReps
	}

	return measure.Condition{
		HarnessVersion:  version,
		HarnessCommit:   commit,
		Engine:          info.Name,
		EngineVersion:   engVersion,
		Plane:           string(info.Plane),
		DialectUsed:     dialects,
		Config:          cfgMap,
		Tuned:           cfg.Tuned,
		Dataset:         ds.Name(),
		Scale:           rc.scale,
		DatasetChecksum: ds.Checksum(),
		ParamsChecksum:  paramsChecksum(ds),
		Workload:        wl.Name,
		MixSeed:         rc.seed,
		LatencyModel:    res.Latency,
		Rate:            rc.rate,
		Concurrency:     conc,
		WarmupOutcome:   warmupOutcome,
		Cache:           cache,
		ColdProtocol:    coldProtocol,
		ValidationMode:  validation,
		Repetitions:     reps,
		Hardware:        measure.CollectHardware(),
		StartedAt:       startedAt,
		FinishedAt:      finishedAt,
	}
}

// paramsChecksum hashes the dataset's params.json, so the stamp pins which
// parameter pools the run drew from. "none" when the dataset has no pools.
func paramsChecksum(ds engine.Dataset) string {
	if ds.Dir() == "" {
		return "none"
	}
	data, err := os.ReadFile(filepath.Join(ds.Dir(), "params.json"))
	if err != nil {
		return "none"
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}

// startContainerIfNeeded launches a managed server container for Bolt-plane
// engines when no server is configured: NEO4J_URI / MEMGRAPH_URI unset,
// Docker present, and neither --no-docker nor GRAPH_BENCH_SKIP_DOCKER given.
func startContainerIfNeeded(ctx context.Context, engName string, rc runConfig) (*setup.Container, error) {
	if rc.noDocker || os.Getenv("GRAPH_BENCH_SKIP_DOCKER") != "" {
		return nil, nil
	}
	var spec setup.ContainerSpec
	switch engName {
	case "neo4j":
		if os.Getenv("NEO4J_URI") != "" {
			return nil, nil
		}
		spec = setup.Neo4j("")
	case "memgraph":
		if os.Getenv("MEMGRAPH_URI") != "" {
			return nil, nil
		}
		spec = setup.Memgraph("")
	default:
		return nil, nil
	}
	if !dockerAvailable() {
		return nil, nil
	}
	fmt.Fprintf(rc.stderr, "run: starting managed %s container (no %s_URI set)\n", engName, envPrefix(engName))
	c, err := setup.Start(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("%s: managed container: %w", engName, err)
	}
	return c, nil
}

func envPrefix(engName string) string {
	if engName == "memgraph" {
		return "MEMGRAPH"
	}
	return "NEO4J"
}

// dockerAvailable reports whether a docker client binary is on PATH.
func dockerAvailable() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}
