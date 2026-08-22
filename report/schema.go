// Package report owns the schema-3 result document, its append-only lineage
// on disk, and the matrix renderers (table, markdown, CSV) that turn one or
// more documents into the comparison a reader interprets.
//
// One JSON document per (workload, engine, run), schema-versioned, carrying
// the Condition stamp (spec 08 §7), verification outcomes, per-class and
// per-query statistics, the cold section, sweep points, load stats, resources,
// and TEPS where present (spec 09 §2). Readers accept schema 2 (v1 results)
// best-effort for `compare` continuity.
//
// See notes/Spec/2064g/bench/09-cli-reporting-ci.md §2–§3 and
// 08-measurement-and-gates.md §7–§8.
package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/measure"
)

// Schema is the current result document schema version (spec 09 §2).
const Schema = 3

// Verification is one query's verification verdict as plain data. It mirrors
// verify.QueryReport without importing the verify package, so a document can
// be read and rendered without the workload machinery. Outcome is "PASS",
// "FAIL", or "SKIP"; Reason is the machine-readable skip reason or the first
// mismatch diff (spec 08 §5).
type Verification struct {
	QueryID string `json:"query_id"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
	Samples int    `json:"samples"`
	Dialect string `json:"dialect,omitempty"`
}

// ClassStat is the JSON-stable latency distribution for one class or one
// query (spec 08 §1). Duration fields marshal as int64 nanoseconds.
type ClassStat struct {
	Class         string        `json:"class,omitempty"`
	Count         int           `json:"count"`
	Errors        int           `json:"errors"`
	FirstError    string        `json:"first_error,omitempty"`
	Min           time.Duration `json:"min"`
	P50           time.Duration `json:"p50"`
	P90           time.Duration `json:"p90"`
	P95           time.Duration `json:"p95"`
	P99           time.Duration `json:"p99"`
	Max           time.Duration `json:"max"`
	Mean          time.Duration `json:"mean"`
	StdDev        time.Duration `json:"stddev"`
	Throughput    float64       `json:"throughput"`
	RowThroughput float64       `json:"row_throughput"`
}

// LoadDoc is the bulk-load cost (spec 08 §1 metrics 3/4), the JSON-stable
// image of engine.LoadStats.
type LoadDoc struct {
	Duration    time.Duration `json:"duration"`
	Nodes       int64         `json:"nodes"`
	Edges       int64         `json:"edges"`
	BytesOnDisk int64         `json:"bytes_on_disk"`
	Method      string        `json:"method,omitempty"`
}

// SweepPoint is one concurrency point of the latency-under-load curve.
type SweepPoint struct {
	Concurrency int           `json:"concurrency"`
	Class       string        `json:"class"`
	Throughput  float64       `json:"throughput"`
	P99         time.Duration `json:"p99"`
}

// TEPSDoc is the Graph500 traversal-rate section (spec 08 §1 metric 6),
// one entry per traversal kernel: the rate each timed repetition reached
// and their harmonic mean, which is the rate-correct aggregate (the
// arithmetic mean of rates overweights the fast runs).
//
// Rates are per repetition and not per root. The analytics protocol draws
// one source per query per run and repeats the kernel from it, so this
// section says what one traversal costs and how much the repetitions
// varied; Graph500's own 64-root aggregate is the same harmonic mean over
// a run per root, which the curated parameter pools supply.
//
// Edges is what the rate divides: the edges out of every node the source
// reaches, counted by the oracle, which is the edge work a full
// breadth-first traversal from that source does.
type TEPSDoc struct {
	Source       string    `json:"source,omitempty"`
	Edges        int64     `json:"edges"`
	PerRep       []float64 `json:"per_rep"`
	HarmonicMean float64   `json:"harmonic_mean"`
}

// DriftDoc is what one class's latency did over the length of a sustained
// run: the p99 of the first window, of the worst, and the trend from the
// run's first half to its second. A run whose Trend is 1.0 ended the way it
// started; one that degrades as its store grows says so here and nowhere
// else, because a single p99 over the whole run averages the good part in.
type DriftDoc struct {
	Window   time.Duration `json:"window"`
	Windows  int           `json:"windows"`
	FirstP99 time.Duration `json:"first_p99"`
	WorstP99 time.Duration `json:"worst_p99"`
	WorstAt  time.Duration `json:"worst_at"`
	Trend    float64       `json:"trend"`

	// P99s is every window's p99 in order, so a reader can tell a run
	// that drifted from one that wobbled without rerunning it.
	P99s []time.Duration `json:"p99s,omitempty"`
}

// Document is the schema-3 result document: one JSON file per (workload,
// engine, run). Condition and Resource embed the measure types directly —
// they are the contract of record and marshal with their Go field names.
//
// A schema-2 (v1) file read through Read is mapped best-effort into this
// shape and keeps Schema == 2 so downstream code can see the provenance.
type Document struct {
	Schema       int                  `json:"schema"`
	Workload     string               `json:"workload"`
	Family       string               `json:"family,omitempty"`
	Fidelity     string               `json:"fidelity,omitempty"`
	Condition    measure.Condition    `json:"condition"`
	Verification []Verification       `json:"verification,omitempty"`
	Classes      map[string]ClassStat `json:"classes,omitempty"`
	Queries      map[string]ClassStat `json:"queries,omitempty"`
	Cold         map[string]ClassStat `json:"cold,omitempty"`
	Sweep        []SweepPoint         `json:"sweep,omitempty"`
	Load         LoadDoc              `json:"load"`
	Resource     measure.Resource     `json:"resource"`
	TEPS         map[string]TEPSDoc   `json:"teps,omitempty"`
	Drift        map[string]DriftDoc  `json:"drift,omitempty"`
}

// FromMeasure builds a schema-3 Document from a measured result plus the
// verification verdicts the run printed before timing (spec 09 §1). Fidelity
// is the workload's coverage-map cell (spec 07 §6), restated in the rendered
// footer. TEPS carries whatever traversal rates the run measured, which is
// nothing outside the kernel workloads.
func FromMeasure(workload, family, fidelity string, res measure.Result, ver []Verification) *Document {
	doc := &Document{
		Schema:       Schema,
		Workload:     workload,
		Family:       family,
		Fidelity:     fidelity,
		Condition:    res.Condition,
		Verification: ver,
		Classes:      classStats(res.Stats),
		Queries:      queryStats(res.ByQuery),
		Cold:         classStats(res.Cold),
		TEPS:         tepsDocs(res.Traversal),
		Load: LoadDoc{
			Duration:    res.Load.Duration,
			Nodes:       res.Load.Nodes,
			Edges:       res.Load.Edges,
			BytesOnDisk: res.Load.BytesOnDisk,
			Method:      res.Load.Method,
		},
		Resource: res.Resource,
		Drift:    driftDocs(res.Drift),
	}
	for _, p := range res.Sweep {
		doc.Sweep = append(doc.Sweep, SweepPoint{
			Concurrency: p.Concurrency,
			Class:       string(p.Class),
			Throughput:  p.Throughput,
			P99:         p.P99,
		})
	}
	return doc
}

// classStats converts a per-class measure.Stat map to the JSON-stable shape.
func classStats(in map[engine.Class]measure.Stat) map[string]ClassStat {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]ClassStat, len(in))
	for cl, s := range in {
		out[string(cl)] = statDoc(s)
	}
	return out
}

// queryStats converts a per-query measure.Stat map to the JSON-stable shape.
func queryStats(in map[string]measure.Stat) map[string]ClassStat {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]ClassStat, len(in))
	for qid, s := range in {
		out[qid] = statDoc(s)
	}
	return out
}

// tepsDocs converts the measured traversal rates to their JSON-stable shape.
func tepsDocs(in map[string]measure.Traversal) map[string]TEPSDoc {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]TEPSDoc, len(in))
	for qid, t := range in {
		out[qid] = TEPSDoc{
			Source:       t.Source,
			Edges:        t.Edges,
			PerRep:       t.PerRep,
			HarmonicMean: t.HarmonicMean,
		}
	}
	return out
}

// driftDocs converts the per-class drift to its JSON-stable image.
func driftDocs(in map[engine.Class]measure.Drift) map[string]DriftDoc {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]DriftDoc, len(in))
	for class, d := range in {
		out[string(class)] = DriftDoc{
			Window:   d.Window,
			Windows:  d.Windows,
			FirstP99: d.First.P99,
			WorstP99: d.Worst.P99,
			WorstAt:  d.WorstAt,
			Trend:    d.Trend,
			P99s:     d.P99s,
		}
	}
	return out
}

// statDoc converts one measure.Stat to its JSON-stable image.
func statDoc(s measure.Stat) ClassStat {
	return ClassStat{
		Class:         string(s.Class),
		Count:         s.Count,
		Errors:        s.Errors,
		FirstError:    s.FirstError,
		Min:           s.Min,
		P50:           s.P50,
		P90:           s.P90,
		P95:           s.P95,
		P99:           s.P99,
		Max:           s.Max,
		Mean:          s.Mean,
		StdDev:        s.StdDev,
		Throughput:    s.Throughput,
		RowThroughput: s.RowThroughput,
	}
}

// Write appends doc to the lineage under dir at
//
//	<dir>/<workload>/<scale>/<ts>-<engine>-<plane>-<checksum8>.json
//
// where ts is Condition.StartedAt in UTC 20060102T150405Z, and checksum8 is
// the first 8 hex characters of Condition.DatasetChecksum ("nochecksum" when
// absent). The lineage is append-only (spec 09 §2): Write refuses to
// overwrite an existing file and refuses a document whose stamp lacks the
// fields the path is built from. It returns the path written.
func Write(dir string, doc *Document) (string, error) {
	c := doc.Condition
	workload := doc.Workload
	if workload == "" {
		workload = c.Workload
	}
	if workload == "" {
		return "", fmt.Errorf("report: refusing to write document with empty workload")
	}
	if c.Engine == "" {
		return "", fmt.Errorf("report: refusing to write document with empty Condition.Engine")
	}
	if c.StartedAt.IsZero() {
		return "", fmt.Errorf("report: refusing to write document with zero Condition.StartedAt")
	}
	scale := c.Scale
	if scale == "" {
		scale = "unknown"
	}
	name := fmt.Sprintf("%s-%s-%s-%s.json",
		c.StartedAt.UTC().Format("20060102T150405Z"),
		slugify(c.Engine), slugify(c.Plane), checksumPrefix8(c.DatasetChecksum))
	path := filepath.Join(dir, workload, scale, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("report: mkdir %s: %w", filepath.Dir(path), err)
	}
	// O_EXCL is the append-only guarantee: a re-run at the same second with
	// the same stamp is a collision, not a replacement (F9).
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("report: lineage is append-only, refusing to overwrite: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return "", fmt.Errorf("report: encode %s: %w", path, err)
	}
	return path, nil
}

// checksumPrefix8 returns the first 8 hex characters of a checksum, stripping
// a "sha256:" prefix. An empty checksum reads "nochecksum" so the filename
// discloses the missing stamp instead of hiding it behind zeros.
func checksumPrefix8(checksum string) string {
	hex := strings.TrimPrefix(checksum, "sha256:")
	switch {
	case hex == "":
		return "nochecksum"
	case len(hex) >= 8:
		return hex[:8]
	default:
		return hex
	}
}

// slugify lowercases and replaces spaces and slashes so an engine or plane
// name is a safe filename component.
func slugify(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "/", "-")
	return s
}

// UpdateIndex regenerates <resultsDir>/INDEX.md as a sorted table of every
// document found under the lineage tree (spec 09 §2: `--publish` writes the
// record and updates the index). Only the index is regenerated; the lineage
// records themselves are never touched. Files that fail to parse are skipped:
// the index lists documents, it does not gate them.
func UpdateIndex(resultsDir string) error {
	matches, err := filepath.Glob(filepath.Join(resultsDir, "*", "*", "*.json"))
	if err != nil {
		return fmt.Errorf("report: glob lineage: %w", err)
	}
	type indexRow struct {
		workload, scale, engine, plane, date, rel string
	}
	var rows []indexRow
	for _, path := range matches {
		doc, err := Read(path)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(resultsDir, path)
		if err != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		workload := doc.Workload
		if workload == "" {
			workload = doc.Condition.Workload
		}
		date := indexDate(doc, filepath.Base(path))
		rows = append(rows, indexRow{
			workload: workload,
			scale:    doc.Condition.Scale,
			engine:   doc.Condition.Engine,
			plane:    doc.Condition.Plane,
			date:     date,
			rel:      rel,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].workload != rows[j].workload {
			return rows[i].workload < rows[j].workload
		}
		if rows[i].scale != rows[j].scale {
			return rows[i].scale < rows[j].scale
		}
		return rows[i].rel < rows[j].rel
	})
	var b strings.Builder
	b.WriteString("# Results index\n\n")
	b.WriteString("Regenerated by `graph-bench run --publish`; do not edit by hand.\n")
	b.WriteString("Records are append-only (spec 09 §2).\n\n")
	b.WriteString("| Workload | Scale | Engine | Plane | Date | File |\n")
	b.WriteString("|----------|-------|--------|-------|------|------|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | [%s](%s) |\n",
			r.workload, r.scale, r.engine, r.plane, r.date, filepath.Base(r.rel), r.rel)
	}
	if err := os.WriteFile(filepath.Join(resultsDir, "INDEX.md"), []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("report: write INDEX.md: %w", err)
	}
	return nil
}

// indexDate picks the index date column: the Condition timestamp when
// present, else the filename's timestamp prefix, else "unknown".
func indexDate(doc *Document, base string) string {
	if !doc.Condition.StartedAt.IsZero() {
		return doc.Condition.StartedAt.UTC().Format("2006-01-02")
	}
	if t, err := time.Parse("20060102T150405Z", strings.SplitN(base, "-", 2)[0]); err == nil {
		return t.Format("2006-01-02")
	}
	return "unknown"
}

// Read loads one result document. Schema-3 files decode directly. A file
// without a schema field (or with schema < 3) is a v1 record: it is mapped
// best-effort into Document — class/query stats plus the condition subset v1
// stamped — and kept marked Schema == 2 (spec 09 §2: readers accept schema 2
// for compare continuity). Unknown fields never error.
func Read(path string) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("report: read %s: %w", path, err)
	}
	var probe struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("report: parse %s: %w", path, err)
	}
	if probe.Schema >= Schema {
		var doc Document
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("report: parse schema-%d %s: %w", probe.Schema, path, err)
		}
		return &doc, nil
	}
	return readV1(data, path)
}

// v1Stat is the schema-2 (v1 measure.Stat) JSON shape: PascalCase fields,
// numeric class, durations as bare nanoseconds. All fields are optional.
type v1Stat struct {
	Class         int
	Count         int
	Errors        int
	Min           time.Duration
	P50           time.Duration
	P90           time.Duration
	P95           time.Duration
	P99           time.Duration
	Max           time.Duration
	Mean          time.Duration
	StdDev        time.Duration
	Throughput    float64
	RowThroughput float64
}

// v1Doc is the schema-2 top-level shape as written by v1's lineage Append
// (see the committed records under results/micro-grid/SF1).
type v1Doc struct {
	Stats   map[string]v1Stat
	ByQuery map[string]v1Stat
	Cold    map[string]v1Stat
	Load    struct {
		Duration    time.Duration
		BytesOnDisk int64
		Nodes       int64
		Edges       int64
		Method      string
	}
	Condition struct {
		Engine          string
		EngineVersion   string
		Plane           string
		Config          map[string]string
		Tuned           bool
		HarnessVersion  string
		HarnessCommit   string
		Dataset         string
		Scale           string
		DatasetChecksum string
		Workload        string
		Cache           string
		OfferedRate     float64
		Concurrency     []int
		Hardware        string
		OS              string
		Repetitions     int
		Seed            int64
		Warmup          string
		Timestamp       time.Time
	}
}

// readV1 maps a v1 record into a Document, best effort, marked Schema == 2.
//
// Mapping decisions:
//   - v1 classes were an iota enum (0 PointRead, 1 Traversal, 2 Subgraph,
//     3 Write, 4 Analytical — v1 had no Aggregation); both the map keys and
//     the Class field are translated to the v2 string names.
//   - Condition: Timestamp→StartedAt, OfferedRate→Rate, Warmup→WarmupOutcome,
//     Seed→MixSeed; the "os/arch" string is split into Hardware.OS/Arch. v1
//     never stamped a latency model — the field stays empty rather than guessed.
//   - Verification, Sweep points, Resource, and TEPS did not exist in v1 and
//     stay zero.
func readV1(data []byte, path string) (*Document, error) {
	var v1 v1Doc
	if err := json.Unmarshal(data, &v1); err != nil {
		return nil, fmt.Errorf("report: parse schema-2 %s: %w", path, err)
	}
	doc := &Document{
		Schema:   2,
		Workload: v1.Condition.Workload,
		Classes:  v1ClassMap(v1.Stats),
		Queries:  v1QueryMap(v1.ByQuery),
		Cold:     v1ClassMap(v1.Cold),
		Load: LoadDoc{
			Duration:    v1.Load.Duration,
			Nodes:       v1.Load.Nodes,
			Edges:       v1.Load.Edges,
			BytesOnDisk: v1.Load.BytesOnDisk,
			Method:      v1.Load.Method,
		},
	}
	c := &doc.Condition
	c.Engine = v1.Condition.Engine
	c.EngineVersion = v1.Condition.EngineVersion
	c.Plane = v1.Condition.Plane
	c.Config = v1.Condition.Config
	c.Tuned = v1.Condition.Tuned
	c.HarnessVersion = v1.Condition.HarnessVersion
	c.HarnessCommit = v1.Condition.HarnessCommit
	c.Dataset = v1.Condition.Dataset
	c.Scale = v1.Condition.Scale
	c.DatasetChecksum = v1.Condition.DatasetChecksum
	c.Workload = v1.Condition.Workload
	c.Cache = v1.Condition.Cache
	c.Rate = v1.Condition.OfferedRate
	c.Concurrency = v1.Condition.Concurrency
	c.Repetitions = v1.Condition.Repetitions
	c.MixSeed = v1.Condition.Seed
	c.WarmupOutcome = v1.Condition.Warmup
	c.StartedAt = v1.Condition.Timestamp
	c.Hardware.CPU = v1.Condition.Hardware
	if osArch := strings.SplitN(v1.Condition.OS, "/", 2); len(osArch) == 2 {
		c.Hardware.OS = osArch[0]
		c.Hardware.Arch = osArch[1]
	} else {
		c.Hardware.OS = v1.Condition.OS
	}
	return doc, nil
}

// v1ClassName translates the v1 numeric class enum to the v2 string name.
func v1ClassName(n int) string {
	switch n {
	case 0:
		return string(engine.PointRead)
	case 1:
		return string(engine.Traversal)
	case 2:
		return string(engine.Subgraph)
	case 3:
		return string(engine.Write)
	case 4:
		return string(engine.Analytical)
	}
	return fmt.Sprintf("class-%d", n)
}

// v1ClassMap converts a v1 class-keyed stat map. v1 keyed by the numeric
// class as a string ("1"); non-numeric keys are kept as-is.
func v1ClassMap(in map[string]v1Stat) map[string]ClassStat {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]ClassStat, len(in))
	for key, s := range in {
		label := key
		if n, err := strconv.Atoi(key); err == nil {
			label = v1ClassName(n)
		}
		out[label] = v1StatDoc(s)
	}
	return out
}

// v1QueryMap converts a v1 query-id-keyed stat map.
func v1QueryMap(in map[string]v1Stat) map[string]ClassStat {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]ClassStat, len(in))
	for qid, s := range in {
		out[qid] = v1StatDoc(s)
	}
	return out
}

// v1StatDoc converts one v1 stat, translating the numeric class.
func v1StatDoc(s v1Stat) ClassStat {
	return ClassStat{
		Class:         v1ClassName(s.Class),
		Count:         s.Count,
		Errors:        s.Errors,
		Min:           s.Min,
		P50:           s.P50,
		P90:           s.P90,
		P95:           s.P95,
		P99:           s.P99,
		Max:           s.Max,
		Mean:          s.Mean,
		StdDev:        s.StdDev,
		Throughput:    s.Throughput,
		RowThroughput: s.RowThroughput,
	}
}
