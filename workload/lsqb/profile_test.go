package lsqb_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"testing"
	"time"

	gradapter "github.com/tamnd/graph-bench/adapter/gr"
	"github.com/tamnd/graph-bench/dataset/ldbc"
	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
)

// These tests run gr on the real LDBC SNB SF1 graph, the only scale where the
// LSQB query costs are representative. They are gated behind GRAPH_BENCH_SF1
// because the first run downloads a multi-GB archive and pays a one-time bulk
// load; both are cached under the user cache dir, so re-entry is fast. They are
// the profiling inner loop the "profile before patching" rule asks for.
//
// TestFetchSF1 provisions and verifies the dataset.
// TestProfileLSQBOnSF1 loads it into gr once (caching the loaded database on
// disk so later runs skip the load), times every LSQB query, and dumps CPU
// profiles for the load phase and the query phase so the dominant cost is a
// flame graph, not a guess. The oracle cross-check is gated behind
// GRAPH_BENCH_ORACLE because computing it over 17M edges is itself slow and is
// not needed when iterating on query latency.

// cacheBase is the graph-bench cache root, where the loaded gr database is kept
// between runs.
func cacheBase(t *testing.T) string {
	t.Helper()
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "graph-bench")
}

// sf1Dataset fetches the pinned SF1 dataset (cache hit after the first run),
// reporting download progress to stderr so a long fetch is not a silent hang.
func sf1Dataset(t *testing.T) target.Dataset {
	t.Helper()
	pin, err := ldbc.LoadPin("SF1")
	if err != nil {
		t.Fatalf("LoadPin: %v", err)
	}
	start := time.Now()
	lastPct := -1
	ds, err := ldbc.Fetch(context.Background(), pin, &ldbc.FetchOptions{
		HTTPTimeout: 60 * time.Minute,
		Progress: func(done, total int64) {
			if total <= 0 {
				return
			}
			pct := int(done * 100 / total)
			if pct != lastPct && pct%5 == 0 {
				lastPct = pct
				fmt.Fprintf(os.Stderr, "  download %3d%%  %6.1f / %6.1f MiB  %s\n",
					pct, float64(done)/(1<<20), float64(total)/(1<<20), time.Since(start).Round(time.Second))
			}
		},
	})
	if err != nil {
		t.Fatalf("Fetch SF1: %v", err)
	}
	sc := ds.Schema()
	t.Logf("SF1 ready in %s: %d node labels, %d rel types", time.Since(start).Round(time.Second),
		len(sc.Nodes), len(sc.Relationships))
	return ds
}

// TestFetchSF1 provisions and verifies the dataset, nothing more. Run this first
// to pay the download once and confirm it fits on disk before profiling.
func TestFetchSF1(t *testing.T) {
	if os.Getenv("GRAPH_BENCH_SF1") == "" {
		t.Skip("set GRAPH_BENCH_SF1=1 to provision the real LDBC SF1 dataset")
	}
	ds := sf1Dataset(t)
	t.Logf("schema: %d node labels, %d rel types", len(ds.Schema().Nodes), len(ds.Schema().Relationships))
}

// loadedGR opens a gr database at a stable cache path and ensures it is populated
// with SF1. If the cached database already holds the node count the pin records,
// the load is skipped; otherwise the dataset is bulk-loaded and a CPU profile of
// the load is written. It returns the open driver and the teardown.
func loadedGR(t *testing.T, ds target.Dataset, wantNodes int64) (target.Driver, func()) {
	t.Helper()
	dbDir := filepath.Join(cacheBase(t), "gr")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("mkdir gr cache: %v", err)
	}
	dbPath := filepath.Join(dbDir, "sf1.gr")

	tgt := gradapter.New()
	ctx := context.Background()
	drv, err := tgt.Setup(ctx, target.Config{Values: map[string]string{"path": dbPath}})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	teardown := func() { _ = tgt.Teardown(ctx, drv) }

	if nodeCount(t, drv) == wantNodes {
		t.Logf("reusing cached gr database at %s (%d nodes)", dbPath, wantNodes)
		return drv, teardown
	}

	// Not loaded (fresh or partial): rebuild from scratch.
	teardown()
	_ = os.Remove(dbPath)
	_ = os.Remove(dbPath + "-wal")
	drv, err = tgt.Setup(ctx, target.Config{Values: map[string]string{"path": dbPath}})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	teardown = func() { _ = tgt.Teardown(ctx, drv) }

	prof, err := os.Create("lsqb-sf1-load.prof")
	if err != nil {
		t.Fatalf("create load profile: %v", err)
	}
	runtime.GC()
	if err := pprof.StartCPUProfile(prof); err != nil {
		t.Fatalf("start load profile: %v", err)
	}
	loadStart := time.Now()
	stats, err := tgt.Load(ctx, drv, ds)
	pprof.StopCPUProfile()
	prof.Close()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Logf("gr load: %s for %d nodes, %d edges (%s)", time.Since(loadStart).Round(time.Millisecond),
		stats.Nodes, stats.Edges, "profile: lsqb-sf1-load.prof")
	return drv, teardown
}

// nodeCount runs MATCH (n) RETURN count(n) and returns the total, or -1 on any
// error (an unpopulated or fresh database).
func nodeCount(t *testing.T, drv target.Driver) int64 {
	t.Helper()
	res, err := drv.Run(context.Background(), target.Query{Text: "MATCH (n) RETURN count(n) AS c"}, nil)
	if err != nil {
		return -1
	}
	defer res.Close()
	if !res.Next() {
		return -1
	}
	switch v := res.Row()[0].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	default:
		return -1
	}
}

// TestProfileLSQBOnSF1 loads SF1 into gr (cached) and times every LSQB query,
// writing a CPU profile of the query phase to lsqb-sf1-query.prof. It prints a
// timing table so the slowest query is obvious. With GRAPH_BENCH_ORACLE set it
// also validates each gr count against the engine-independent oracle, the first
// time the oracle runs on real SF1.
func TestProfileLSQBOnSF1(t *testing.T) {
	if os.Getenv("GRAPH_BENCH_SF1") == "" {
		t.Skip("set GRAPH_BENCH_SF1=1 to profile gr on the real LDBC SF1 dataset")
	}
	ds := sf1Dataset(t)
	pin, _ := ldbc.LoadPin("SF1")

	wl, ok := workload.Lookup("lsqb")
	if !ok {
		t.Fatal("workload lsqb not registered")
	}

	drv, teardown := loadedGR(t, ds, pin.NodeCount)
	defer teardown()

	// Per-query timeout so one pathological plan does not block the whole run; gr
	// is given the deadline through the context and we record a timeout rather
	// than hanging. Tunable through GRAPH_BENCH_QUERY_TIMEOUT (seconds).
	timeout := 90 * time.Second
	if s := os.Getenv("GRAPH_BENCH_QUERY_TIMEOUT"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			timeout = d
		}
	}
	runCount := func(text string) (int64, time.Duration, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		st := time.Now()
		res, err := drv.Run(ctx, target.Query{Text: text, Class: target.Subgraph}, nil)
		if err != nil {
			return -1, time.Since(st), true
		}
		var n int64
		for res.Next() {
			switch v := res.Row()[0].(type) {
			case int64:
				n = v
			case float64:
				n = int64(v)
			}
		}
		iterErr := res.Err()
		_ = res.Close()
		dur := time.Since(st)
		if iterErr != nil || ctx.Err() != nil {
			return n, dur, true
		}
		return n, dur, false
	}

	profile := os.Getenv("GRAPH_BENCH_QUERY_PROFILE") != ""
	if profile {
		prof, err := os.Create("lsqb-sf1-query.prof")
		if err != nil {
			t.Fatalf("create profile: %v", err)
		}
		defer prof.Close()
		runtime.GC()
		if err := pprof.StartCPUProfile(prof); err != nil {
			t.Fatalf("start profile: %v", err)
		}
		defer pprof.StopCPUProfile()
	}

	reps := 3
	if s := os.Getenv("GRAPH_BENCH_REPS"); s != "" {
		fmt.Sscanf(s, "%d", &reps)
	}
	type timing struct {
		id    string
		count int64
		p50   time.Duration
		timed bool
	}
	// only and skip are comma-separated substring filters on the query id so a run
	// can target a subset (GRAPH_BENCH_ONLY=q7,q8) or step around the pathological
	// ones (GRAPH_BENCH_SKIP=q3,q6) while they are optimized in isolation. Empty
	// means run every query.
	matchAny := func(env, id string) bool {
		for _, part := range strings.Split(env, ",") {
			if p := strings.TrimSpace(part); p != "" && strings.Contains(id, p) {
				return true
			}
		}
		return false
	}
	only := os.Getenv("GRAPH_BENCH_ONLY")
	skip := os.Getenv("GRAPH_BENCH_SKIP")
	selected := func(id string) bool {
		if only != "" && !matchAny(only, id) {
			return false
		}
		if skip != "" && matchAny(skip, id) {
			return false
		}
		return true
	}

	var table []timing
	fmt.Fprintf(os.Stderr, "\nLSQB on SF1, gr, streaming (timeout %s, reps %d):\n", timeout, reps)
	for _, q := range wl.Queries {
		if !selected(q.ID) {
			continue
		}
		text := q.Texts[workload.Cypher]
		c, d, to := runCount(text) // warm-up, also the first sample
		samples := []time.Duration{d}
		gotCount := c
		anyTimeout := to
		if !to {
			for i := 0; i < reps-1; i++ {
				c, d, to = runCount(text)
				samples = append(samples, d)
				gotCount = c
				anyTimeout = anyTimeout || to
			}
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		p50 := samples[len(samples)/2]
		table = append(table, timing{q.ID, gotCount, p50, anyTimeout})
		note := ""
		if anyTimeout {
			note = "  TIMEOUT/err"
		}
		fmt.Fprintf(os.Stderr, "  %-9s  p50 %12s  count %d%s\n", q.ID, p50.Round(time.Microsecond), gotCount, note)
	}

	sort.Slice(table, func(i, j int) bool { return table[i].p50 > table[j].p50 })
	fmt.Fprintf(os.Stderr, "\nslowest first:\n")
	for _, r := range table {
		fmt.Fprintf(os.Stderr, "  %-9s  p50 %12s  count %d\n", r.id, r.p50.Round(time.Microsecond), r.count)
	}
	if profile {
		fmt.Fprintf(os.Stderr, "CPU profile: workload/lsqb/lsqb-sf1-query.prof\n")
	}

	if os.Getenv("GRAPH_BENCH_ORACLE") == "" {
		t.Log("skipping oracle cross-check; set GRAPH_BENCH_ORACLE=1 to validate counts on SF1")
		return
	}
	for _, q := range wl.Queries {
		if !selected(q.ID) {
			continue
		}
		ost := time.Now()
		want, err := q.Reference.Compute(ds, nil)
		if err != nil {
			t.Errorf("%s oracle: %v", q.ID, err)
			continue
		}
		oracleCount := want.Rows[0][0].(int64)
		var gr int64
		for _, r := range table {
			if r.id == q.ID {
				gr = r.count
			}
		}
		status := "ok"
		if gr != oracleCount {
			status = "MISMATCH"
			t.Errorf("%s: gr %d != oracle %d", q.ID, gr, oracleCount)
		}
		fmt.Fprintf(os.Stderr, "  %-9s  oracle %d  gr %d  %s  (%s)\n",
			q.ID, oracleCount, gr, status, time.Since(ost).Round(time.Millisecond))
	}
}
