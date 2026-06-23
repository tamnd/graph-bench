package report_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/graph-bench/measure"
	"github.com/tamnd/graph-bench/report"
	"github.com/tamnd/graph-bench/target"
)

// makeEngineResult builds a test EngineResult with a single class stat.
func makeEngineResult(name, plane string, class target.Class, p50, p99 time.Duration, tput float64) report.EngineResult {
	return report.EngineResult{
		Name:  name,
		Plane: plane,
		Result: measure.Result{
			Stats: map[target.Class]measure.Stat{
				class: {Class: class, Count: 100, P50: p50, P99: p99, Throughput: tput},
			},
		},
	}
}

// TestAssembleMatrixRowOrder proves in-process engines come before Bolt in the
// assembled matrix (F3, doc 08 section 4.4).
func TestAssembleMatrixRowOrder(t *testing.T) {
	results := []report.EngineResult{
		makeEngineResult("neo4j", "bolt", target.PointRead, 50*time.Microsecond, 200*time.Microsecond, 100),
		makeEngineResult("gr", "inproc", target.PointRead, 10*time.Microsecond, 40*time.Microsecond, 500),
	}
	m := report.Assemble(results, report.ColumnClass, "")
	if len(m.Rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(m.Rows))
	}
	if m.Rows[0].Plane != "inproc" {
		t.Errorf("first row plane=%q, want inproc (in-process before Bolt)", m.Rows[0].Plane)
	}
	if m.Rows[1].Plane != "bolt" {
		t.Errorf("second row plane=%q, want bolt", m.Rows[1].Plane)
	}
}

// TestAssembleMatrixColumns proves the column list includes only classes seen
// in the results, in canonical order (PointRead, Traversal, ...).
func TestAssembleMatrixColumns(t *testing.T) {
	results := []report.EngineResult{
		makeEngineResult("gr", "inproc", target.Traversal, 1*time.Millisecond, 5*time.Millisecond, 50),
		makeEngineResult("neo4j", "bolt", target.PointRead, 50*time.Microsecond, 200*time.Microsecond, 100),
	}
	m := report.Assemble(results, report.ColumnClass, "")
	// PointRead and Traversal seen; PointRead should come first.
	if len(m.Columns) < 2 {
		t.Fatalf("want at least 2 columns, got %d", len(m.Columns))
	}
	if m.Columns[0] != "PointRead" {
		t.Errorf("first column=%q, want PointRead", m.Columns[0])
	}
	if m.Columns[1] != "Traversal" {
		t.Errorf("second column=%q, want Traversal", m.Columns[1])
	}
}

// TestAssembleMatrixEmptyCell proves an engine missing a class gets an empty
// cell.
func TestAssembleMatrixEmptyCell(t *testing.T) {
	// neo4j only has PointRead; gr only has Traversal.
	neoResult := makeEngineResult("neo4j", "bolt", target.PointRead, 50*time.Microsecond, 200*time.Microsecond, 100)
	grResult := makeEngineResult("gr", "inproc", target.Traversal, 1*time.Millisecond, 5*time.Millisecond, 50)
	results := []report.EngineResult{neoResult, grResult}
	m := report.Assemble(results, report.ColumnClass, "")
	for _, row := range m.Rows {
		for col, cell := range row.Cells {
			if row.Name == "neo4j" && col == "Traversal" {
				if !cell.Empty {
					t.Error("neo4j Traversal cell should be empty")
				}
			}
			if row.Name == "gr" && col == "PointRead" {
				if !cell.Empty {
					t.Error("gr PointRead cell should be empty")
				}
			}
		}
	}
}

// TestAssembleCellMetrics proves cell P99 and P50 come from the Stats map.
func TestAssembleCellMetrics(t *testing.T) {
	er := makeEngineResult("gr", "inproc", target.PointRead, 10*time.Microsecond, 41*time.Microsecond, 0)
	m := report.Assemble([]report.EngineResult{er}, report.ColumnClass, "")
	if len(m.Rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(m.Rows))
	}
	cell := m.Rows[0].Cells["PointRead"]
	if cell.Metric != 41*time.Microsecond {
		t.Errorf("cell.Metric=%v, want 41µs", cell.Metric)
	}
	if cell.P50 != 10*time.Microsecond {
		t.Errorf("cell.P50=%v, want 10µs", cell.P50)
	}
}

// TestRenderTable proves the table renderer emits engine and plane columns.
func TestRenderTable(t *testing.T) {
	er := makeEngineResult("gr", "inproc", target.PointRead, 10*time.Microsecond, 41*time.Microsecond, 200)
	m := report.Assemble([]report.EngineResult{er}, report.ColumnClass, "snb-short SF1 warm")
	var buf bytes.Buffer
	if err := report.Render(m, report.FormatTable, &buf); err != nil {
		t.Fatalf("Render table: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "gr") {
		t.Error("table output should contain engine name gr")
	}
	if !strings.Contains(out, "inproc") {
		t.Error("table output should contain plane inproc")
	}
	if !strings.Contains(out, "PointRead") {
		t.Error("table output should contain column PointRead")
	}
	if !strings.Contains(out, "snb-short SF1 warm") {
		t.Error("table output should contain run conditions")
	}
}

// TestRenderMarkdown proves the markdown renderer emits GFM table syntax.
func TestRenderMarkdown(t *testing.T) {
	er := makeEngineResult("neo4j", "bolt", target.Traversal, 5*time.Millisecond, 20*time.Millisecond, 50)
	m := report.Assemble([]report.EngineResult{er}, report.ColumnClass, "")
	var buf bytes.Buffer
	if err := report.Render(m, report.FormatMarkdown, &buf); err != nil {
		t.Fatalf("Render markdown: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "| Engine |") {
		t.Error("markdown should start with | Engine |")
	}
	if !strings.Contains(out, "| neo4j |") {
		t.Error("markdown should contain | neo4j |")
	}
	if !strings.Contains(out, "----") {
		t.Error("markdown should contain separator row")
	}
}

// TestRenderCSV proves the CSV renderer emits a header and one data row per
// non-empty cell.
func TestRenderCSV(t *testing.T) {
	er := makeEngineResult("gr", "inproc", target.Write, 1*time.Millisecond, 2*time.Millisecond, 10)
	m := report.Assemble([]report.EngineResult{er}, report.ColumnClass, "")
	var buf bytes.Buffer
	if err := report.Render(m, report.FormatCSV, &buf); err != nil {
		t.Fatalf("Render csv: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("CSV should have header + at least 1 data row, got %d lines", len(lines))
	}
	if !strings.Contains(lines[0], "engine") {
		t.Errorf("CSV header should contain 'engine': %s", lines[0])
	}
	if !strings.Contains(lines[1], "gr") {
		t.Errorf("CSV data row should contain engine 'gr': %s", lines[1])
	}
}

// TestRenderJSONRoundTrip proves RenderJSON + ParseJSON returns equivalent
// results.
func TestRenderJSONRoundTrip(t *testing.T) {
	er := makeEngineResult("memgraph", "bolt", target.Traversal, 3*time.Millisecond, 15*time.Millisecond, 80)
	er.Version = "2.12.0"
	var buf bytes.Buffer
	if err := report.RenderJSON([]report.EngineResult{er}, &buf); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	got, err := report.ParseJSON(&buf)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 result, got %d", len(got))
	}
	if got[0].Name != "memgraph" {
		t.Errorf("Name=%q, want memgraph", got[0].Name)
	}
	if got[0].Plane != "bolt" {
		t.Errorf("Plane=%q, want bolt", got[0].Plane)
	}
	if got[0].Version != "2.12.0" {
		t.Errorf("Version=%q, want 2.12.0", got[0].Version)
	}
	stat := got[0].Result.Stats[target.Traversal]
	if stat.P99 != 15*time.Millisecond {
		t.Errorf("P99=%v, want 15ms", stat.P99)
	}
}

// TestPlaneOverheadComputed proves PlaneOverhead returns the absolute and
// relative overhead between an inproc and Bolt row.
func TestPlaneOverheadComputed(t *testing.T) {
	grInproc := makeEngineResult("gr", "inproc", target.PointRead, 5*time.Microsecond, 20*time.Microsecond, 0)
	grBolt := makeEngineResult("gr-bolt", "bolt", target.PointRead, 60*time.Microsecond, 100*time.Microsecond, 0)
	m := report.Assemble([]report.EngineResult{grInproc, grBolt}, report.ColumnClass, "")
	po := m.PlaneOverhead("gr", "gr-bolt")
	if po == nil {
		t.Fatal("PlaneOverhead returned nil")
	}
	ov, ok := po.Overhead["PointRead"]
	if !ok {
		t.Fatal("no PointRead overhead")
	}
	// absolute = 100µs - 20µs = 80µs
	if ov.Absolute != 80*time.Microsecond {
		t.Errorf("Absolute=%v, want 80µs", ov.Absolute)
	}
	// relative = 100/20 = 5.0
	if ov.Relative < 4.9 || ov.Relative > 5.1 {
		t.Errorf("Relative=%.2f, want ~5.0", ov.Relative)
	}
}

// TestPlaneOverheadMissingEngine proves PlaneOverhead returns nil when either
// engine is not in the matrix.
func TestPlaneOverheadMissingEngine(t *testing.T) {
	er := makeEngineResult("gr", "inproc", target.PointRead, 5*time.Microsecond, 20*time.Microsecond, 0)
	m := report.Assemble([]report.EngineResult{er}, report.ColumnClass, "")
	if po := m.PlaneOverhead("gr", "gr-bolt"); po != nil {
		t.Errorf("expected nil when gr-bolt is missing, got %+v", po)
	}
}

// TestLineageChecksumPrefix8 proves the checksum prefix function extracts
// correctly via the exported RecordPath (which uses checksumPrefix8 internally).
func TestLineageRecordPath(t *testing.T) {
	er := report.EngineResult{
		Name:  "gr",
		Plane: "inproc",
		Result: measure.Result{
			Condition: measure.Condition{
				Engine:          "gr",
				Plane:           "inproc",
				Workload:        "snb-short",
				Scale:           "SF1",
				DatasetChecksum: "sha256:abcdef0123456789abcdef01",
			},
		},
	}
	ts := time.Date(2026, 6, 20, 11, 0, 0, 0, time.UTC)
	path := report.RecordPath("results", er, ts)
	if !strings.Contains(path, "snb-short") {
		t.Errorf("path should contain workload: %s", path)
	}
	if !strings.Contains(path, "SF1") {
		t.Errorf("path should contain scale: %s", path)
	}
	if !strings.Contains(path, "gr") {
		t.Errorf("path should contain engine name: %s", path)
	}
	if !strings.Contains(path, "abcdef01") {
		t.Errorf("path should contain first 8 chars of checksum hex: %s", path)
	}
}

// TestLineageAppendRefusesIncomplete proves Append rejects a record with an
// empty Condition.
func TestLineageAppendRefusesIncomplete(t *testing.T) {
	er := report.EngineResult{
		Name:   "gr",
		Plane:  "inproc",
		Result: measure.Result{
			// Condition is zero: Engine, Dataset, Workload all empty.
		},
	}
	path := t.TempDir() + "/results/snb-short/SF1/test.json"
	if err := report.Append(path, er); err == nil {
		t.Error("Append should fail for a record with an empty Condition")
	}
}
