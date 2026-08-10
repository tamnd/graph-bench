package report

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/measure"
)

// sampleResult builds a fully-stamped measure.Result for round-trip tests.
func sampleResult(engineName, plane string, base time.Duration) measure.Result {
	return measure.Result{
		Stats: map[engine.Class]measure.Stat{
			engine.PointRead: {
				Class: engine.PointRead, Count: 100, Errors: 2,
				Min: base / 2, P50: base, P90: 2 * base, P95: 3 * base,
				P99: 4 * base, Max: 5 * base, Mean: base, StdDev: base / 4,
				Throughput: 1234.5, RowThroughput: 2469.0,
			},
			engine.Analytical: {
				Class: engine.Analytical, Count: 5,
				Min: 2 * time.Second, P50: 3 * time.Second, P99: 4 * time.Second,
				Max: 4 * time.Second, Mean: 3 * time.Second,
			},
		},
		ByQuery: map[string]measure.Stat{
			"is1": {Class: engine.PointRead, Count: 50, Min: base / 2, P50: base, P99: 4 * base, Max: 5 * base, Mean: base},
			"q9":  {Class: engine.Analytical, Count: 5, P50: 3 * time.Second, P99: 4 * time.Second},
		},
		Cold: map[engine.Class]measure.Stat{
			engine.PointRead: {Class: engine.PointRead, Count: 10, P50: 10 * base, P99: 20 * base},
		},
		Load:  engine.LoadStats{Duration: 90 * time.Millisecond, Nodes: 1000, Edges: 5000, BytesOnDisk: 1 << 20, Method: "copy"},
		Sweep: []measure.SweepPoint{{Concurrency: 4, Class: engine.PointRead, Throughput: 900, P99: 6 * base}},
		Resource: measure.Resource{
			HeapAllocBytes: 1 << 24, MaxRSSBytes: 1 << 26, DatasetBytes: 1 << 22, LoadBytes: 1 << 20,
		},
		Latency: measure.ServiceTimeLatency,
		Condition: measure.Condition{
			HarnessVersion:  "0.3.0",
			HarnessCommit:   "abc1234",
			Engine:          engineName,
			EngineVersion:   "1.2.3",
			Plane:           plane,
			Dataset:         "snb-sf1",
			Scale:           "SF1",
			DatasetChecksum: "sha256:fd000c2a1c8fe54f57761318492dd1c481374ae74f58a88cebffb272b5b79337",
			Workload:        "snb-short",
			LatencyModel:    measure.ServiceTimeLatency,
			WarmupOutcome:   "stable",
			Cache:           "warm",
			StartedAt:       time.Date(2026, 8, 10, 12, 30, 45, 0, time.UTC),
			FinishedAt:      time.Date(2026, 8, 10, 12, 35, 0, 0, time.UTC),
		},
	}
}

func sampleVerification() []Verification {
	return []Verification{
		{QueryID: "is1", Outcome: "PASS", Samples: 4, Dialect: "cypher"},
		{QueryID: "is2", Outcome: "SKIP", Reason: "no-dialect-text", Samples: 0},
		{QueryID: "q9", Outcome: "PASS", Samples: 2, Dialect: "cypher"},
	}
}

// TestWriteReadRoundTrip writes a schema-3 document to the lineage and reads
// it back, checking the lineage filename shape and stat fidelity.
func TestWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	doc := FromMeasure("snb-short", "snb", "spec-following; own scheduler", sampleResult("zu", "inproc", 200*time.Microsecond), sampleVerification())
	if doc.Schema != 3 {
		t.Fatalf("FromMeasure schema = %d, want 3", doc.Schema)
	}

	path, err := Write(dir, doc)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	wantName := "20260810T123045Z-zu-inproc-fd000c2a.json"
	if filepath.Base(path) != wantName {
		t.Errorf("lineage filename = %s, want %s", filepath.Base(path), wantName)
	}
	wantDir := filepath.Join(dir, "snb-short", "SF1")
	if filepath.Dir(path) != wantDir {
		t.Errorf("lineage dir = %s, want %s", filepath.Dir(path), wantDir)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Schema != 3 || got.Workload != "snb-short" || got.Family != "snb" {
		t.Errorf("round-trip header = (%d,%q,%q)", got.Schema, got.Workload, got.Family)
	}
	pr := got.Classes["point-read"]
	if pr.P99 != 800*time.Microsecond || pr.Count != 100 || pr.Errors != 2 {
		t.Errorf("point-read round-trip = %+v", pr)
	}
	if pr.Throughput != 1234.5 || pr.RowThroughput != 2469.0 {
		t.Errorf("throughput round-trip = %v/%v", pr.Throughput, pr.RowThroughput)
	}
	if got.Queries["is1"].P50 != 200*time.Microsecond {
		t.Errorf("query is1 p50 = %v", got.Queries["is1"].P50)
	}
	if got.Cold["point-read"].P99 != 4*time.Millisecond {
		t.Errorf("cold p99 = %v", got.Cold["point-read"].P99)
	}
	if got.Condition.Engine != "zu" || got.Condition.LatencyModel != measure.ServiceTimeLatency {
		t.Errorf("condition round-trip = %q/%q", got.Condition.Engine, got.Condition.LatencyModel)
	}
	if got.Condition.WarmupOutcome != "stable" || !got.Condition.StartedAt.Equal(doc.Condition.StartedAt) {
		t.Errorf("condition warmup/start = %q/%v", got.Condition.WarmupOutcome, got.Condition.StartedAt)
	}
	if len(got.Verification) != 3 || got.Verification[1].Reason != "no-dialect-text" {
		t.Errorf("verification round-trip = %+v", got.Verification)
	}
	if got.Load.Method != "copy" || got.Load.Duration != 90*time.Millisecond {
		t.Errorf("load round-trip = %+v", got.Load)
	}
	if len(got.Sweep) != 1 || got.Sweep[0].Class != "point-read" {
		t.Errorf("sweep round-trip = %+v", got.Sweep)
	}
}

// TestWriteNoOverwrite asserts the lineage is append-only: a second write of
// the same stamp is refused, never replaced.
func TestWriteNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	doc := FromMeasure("snb-short", "snb", "", sampleResult("zu", "inproc", time.Millisecond), nil)
	if _, err := Write(dir, doc); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	if _, err := Write(dir, doc); err == nil {
		t.Fatal("second Write of same stamp succeeded; lineage must be append-only")
	}
}

// TestWriteRefusesIncomplete asserts Write refuses stamps missing the fields
// the lineage path is built from.
func TestWriteRefusesIncomplete(t *testing.T) {
	doc := FromMeasure("", "", "", measure.Result{}, nil)
	if _, err := Write(t.TempDir(), doc); err == nil {
		t.Fatal("Write accepted a document with no workload/engine/timestamp")
	}
}

// TestReadSchema2 reads a real v1 lineage record (copied verbatim from
// results/micro-grid/SF1) and checks the best-effort mapping.
func TestReadSchema2(t *testing.T) {
	doc, err := Read(filepath.Join("testdata", "20260624T101112Z-ladybug-inproc-fd000c2a.json"))
	if err != nil {
		t.Fatalf("Read schema-2: %v", err)
	}
	if doc.Schema != 2 {
		t.Errorf("schema = %d, want 2", doc.Schema)
	}
	// v1 class 1 = Traversal.
	tr, ok := doc.Classes["traversal"]
	if !ok {
		t.Fatalf("classes = %v, want traversal key", doc.Classes)
	}
	if tr.Count != 250 || tr.Errors != 50 || tr.P50 != 73528917*time.Nanosecond {
		t.Errorf("traversal = %+v", tr)
	}
	q, ok := doc.Queries["micro-khop1"]
	if !ok || q.P50 != 15881459*time.Nanosecond || q.Class != "traversal" {
		t.Errorf("micro-khop1 = %+v (ok=%v)", q, ok)
	}
	c := doc.Condition
	if c.Engine != "ladybug" || c.EngineVersion != "0.17.1" || c.Plane != "inproc" {
		t.Errorf("condition engine = %q/%q/%q", c.Engine, c.EngineVersion, c.Plane)
	}
	if c.Workload != "micro-grid" || doc.Workload != "micro-grid" || c.Scale != "SF1" {
		t.Errorf("condition workload/scale = %q/%q", c.Workload, c.Scale)
	}
	if !strings.HasPrefix(c.DatasetChecksum, "sha256:fd000c2a") {
		t.Errorf("checksum = %q", c.DatasetChecksum)
	}
	if c.StartedAt.IsZero() || c.StartedAt.Year() != 2026 {
		t.Errorf("StartedAt = %v", c.StartedAt)
	}
	if c.Hardware.OS != "darwin" || c.Hardware.Arch != "arm64" {
		t.Errorf("hardware os/arch = %q/%q", c.Hardware.OS, c.Hardware.Arch)
	}
	if doc.Load.Duration != 71358333*time.Nanosecond || doc.Load.BytesOnDisk != -1 {
		t.Errorf("load = %+v", doc.Load)
	}
}

// TestUpdateIndex publishes two documents and regenerates INDEX.md.
func TestUpdateIndex(t *testing.T) {
	dir := t.TempDir()
	a := FromMeasure("snb-short", "snb", "", sampleResult("zu", "inproc", time.Millisecond), nil)
	b := FromMeasure("snb-short", "snb", "", sampleResult("neo4j", "bolt", 2*time.Millisecond), nil)
	for _, doc := range []*Document{a, b} {
		if _, err := Write(dir, doc); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := UpdateIndex(dir); err != nil {
		t.Fatalf("UpdateIndex: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "INDEX.md"))
	if err != nil {
		t.Fatalf("read INDEX.md: %v", err)
	}
	idx := string(data)
	for _, want := range []string{
		"| Workload | Scale | Engine | Plane | Date | File |",
		"| snb-short | SF1 | zu | inproc | 2026-08-10 |",
		"| snb-short | SF1 | neo4j | bolt | 2026-08-10 |",
		"(snb-short/SF1/20260810T123045Z-zu-inproc-fd000c2a.json)",
	} {
		if !strings.Contains(idx, want) {
			t.Errorf("INDEX.md missing %q\n%s", want, idx)
		}
	}
	// Sorted: within the same workload/scale, filename order (neo4j < zu).
	if strings.Index(idx, "neo4j-bolt") > strings.Index(idx, "zu-inproc") {
		t.Error("INDEX.md rows not sorted by filename within workload/scale")
	}
	// Regeneration is idempotent.
	if err := UpdateIndex(dir); err != nil {
		t.Fatalf("second UpdateIndex: %v", err)
	}
}

// TestPickUnit covers the four-unit auto-selection ladder.
func TestPickUnit(t *testing.T) {
	cases := []struct {
		vals []time.Duration
		want string
	}{
		{nil, "ms"},
		{[]time.Duration{400 * time.Nanosecond, 600 * time.Nanosecond}, "ns"},
		{[]time.Duration{50 * time.Microsecond, 150 * time.Microsecond}, "µs"},
		{[]time.Duration{2 * time.Millisecond, 40 * time.Millisecond}, "ms"},
		{[]time.Duration{3 * time.Second}, "s"},
	}
	for _, c := range cases {
		if got := pickUnit(c.vals); got != c.want {
			t.Errorf("pickUnit(%v) = %q, want %q", c.vals, got, c.want)
		}
	}
}

// testMatrix builds a two-engine matrix with a SKIP and a FAIL verdict.
func testMatrix() *Matrix {
	fast := FromMeasure("snb-short", "snb", "spec-following; own scheduler",
		sampleResult("zu", "inproc", 200*time.Microsecond),
		[]Verification{
			{QueryID: "is1", Outcome: "PASS", Samples: 4},
			{QueryID: "is2", Outcome: "SKIP", Reason: "no-dialect-text"},
			{QueryID: "q9", Outcome: "PASS", Samples: 2},
		})
	slow := FromMeasure("snb-short", "snb", "spec-following; own scheduler",
		sampleResult("neo4j", "bolt", 300*time.Microsecond),
		[]Verification{
			{QueryID: "is1", Outcome: "PASS", Samples: 4},
			{QueryID: "is2", Outcome: "PASS", Samples: 4},
			{QueryID: "q9", Outcome: "FAIL", Reason: "row 0: got 7, want 9"},
		})
	// is2 passed on neo4j but zu skipped it; q9 failed on neo4j.
	delete(slow.Queries, "q9")
	slow.Queries["is2"] = slow.Queries["is1"]
	return NewMatrix([]*Document{slow, fast})
}

// TestRenderTable asserts the layout and footers by key substrings rather
// than full golden bytes: engine column pairs, plane ordering, unit choice,
// SKIP/FAIL cells, fidelity footer, condition footer.
func TestRenderTable(t *testing.T) {
	m := testMatrix()
	// Plane order: inproc before bolt regardless of input order.
	if m.Docs[0].Condition.Engine != "zu" {
		t.Fatalf("matrix plane order: first doc = %s, want zu (inproc)", m.Docs[0].Condition.Engine)
	}
	var buf bytes.Buffer
	RenderTable(&buf, m)
	out := buf.String()
	for _, want := range []string{
		"zu/inproc",
		"neo4j/bolt",
		"Class / Query",
		"point-read",
		"analytical",
		"point-read (cold)",
		"  is1", // query detail indented under rollups
		"SKIP(no-dialect-text)",
		"FAIL",
		"µs",  // point-read row magnitude picks microseconds
		".00", // sub-unit precision digits present
		"fidelity: snb-short: spec-following; own scheduler",
		"zu/inproc: version 1.2.3, dataset fd000c2a, latency service-time, warmup stable, tuned=false",
		"neo4j/bolt: version 1.2.3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q\n%s", want, out)
		}
	}
	// Class rollups come before per-query detail (Interpretation order).
	if strings.Index(out, "point-read") > strings.Index(out, "is1") {
		t.Error("class rollups must precede per-query detail")
	}
	// Analytical row renders in seconds, not a 4000000000ns blob.
	if !regexp.MustCompile(`3\.000s`).MatchString(out) {
		t.Errorf("analytical row not rendered in seconds\n%s", out)
	}
}

// TestRenderMarkdown asserts the markdown table shape and footers.
func TestRenderMarkdown(t *testing.T) {
	var buf bytes.Buffer
	RenderMarkdown(&buf, testMatrix())
	out := buf.String()
	for _, want := range []string{
		"| Class / Query |",
		"zu/inproc p50 | p99 |",
		"| point-read |",
		"| is1 |",
		"SKIP(no-dialect-text)",
		"FAIL",
		"_fidelity: snb-short: spec-following; own scheduler_",
		"_zu/inproc: version 1.2.3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown missing %q\n%s", want, out)
		}
	}
}

// TestRenderCSV asserts the explicit-column CSV: header, nanosecond values,
// verification outcomes on query rows.
func TestRenderCSV(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderCSV(&buf, testMatrix()); err != nil {
		t.Fatalf("RenderCSV: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"engine,plane,section,row,outcome,count,errors,min_ns,p50_ns,p90_ns,p95_ns,p99_ns,max_ns,mean_ns,stddev_ns,throughput,row_throughput",
		"zu,inproc,class,point-read,,100,2,100000,200000,400000,600000,800000,1000000,200000,50000,1234.50,2469.00",
		"zu,inproc,cold,point-read,",
		"zu,inproc,query,is2,SKIP(no-dialect-text)",
		"neo4j,bolt,query,q9,FAIL",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("csv missing %q\n%s", want, out)
		}
	}
}

// TestRenderOverhead builds the same engine on two planes and asserts the
// plane-overhead table (spec 08 §8): p50 per plane, delta and ratio against
// the fastest plane.
func TestRenderOverhead(t *testing.T) {
	inproc := FromMeasure("micro-read", "micro", "harness-native", sampleResult("gr", "inproc", 100*time.Microsecond), nil)
	bolt := FromMeasure("micro-read", "micro", "harness-native", sampleResult("gr", "bolt", 350*time.Microsecond), nil)
	var buf bytes.Buffer
	RenderOverhead(&buf, []*Document{bolt, inproc})
	out := buf.String()
	for _, want := range []string{
		"Query",
		"gr/inproc p50",
		"gr/bolt p50",
		"is1",
		"100.0µs",                   // fastest plane: bare p50
		"350.0µs (+250.0µs, x3.50)", // slower plane: delta vs fastest
	} {
		if !strings.Contains(out, want) {
			t.Errorf("overhead table missing %q\n%s", want, out)
		}
	}
	// Nothing shared: disclosed, not silently empty.
	buf.Reset()
	RenderOverhead(&buf, []*Document{inproc})
	if !strings.Contains(buf.String(), "no query measured on more than one plane") {
		t.Errorf("empty overhead not disclosed:\n%s", buf.String())
	}
}
