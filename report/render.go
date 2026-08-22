// Matrix renderers (spec 09 §3): one column pair (p50/p99) per engine, unit
// auto-selection per row, SKIP/FAIL cells rendered with reasons, class
// rollups first and per-query detail after, fidelity footer (07 §6) and
// condition footer. Ported and adapted from v1's proven table/markdown/CSV
// renderers; the layout changed from engine-rows to engine-columns so classes
// and queries read down the page in Interpretation order.

package report

import (
	"encoding/csv"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/measure"
)

// Matrix is the comparison grid assembled from one document per engine:
// engine columns in plane order (inproc first, then bolt, subprocess,
// native — F3), class rows in canonical order, query rows sorted by id.
// Query rows include ids that only appear in verification (a query skipped
// everywhere still gets its SKIP row — a SKIP is respectable, a silent
// omission is not).
type Matrix struct {
	Docs    []*Document // one column pair per engine, plane-ordered
	Classes []string    // class row labels, canonical order, present somewhere
	Queries []string    // query row labels, sorted
}

// NewMatrix assembles a Matrix from per-engine documents. The documents
// normally share a workload; nothing enforces it, the footers disclose it.
func NewMatrix(docs []*Document) *Matrix {
	m := &Matrix{Docs: make([]*Document, len(docs))}
	copy(m.Docs, docs)
	planeRank := map[string]int{"inproc": 0, "bolt": 1, "subprocess": 2, "native": 3}
	sort.SliceStable(m.Docs, func(i, j int) bool {
		return planeRank[m.Docs[i].Condition.Plane] < planeRank[m.Docs[j].Condition.Plane]
	})

	seenClass := map[string]bool{}
	seenQuery := map[string]bool{}
	for _, d := range m.Docs {
		for cl := range d.Classes {
			seenClass[cl] = true
		}
		for cl := range d.Cold {
			seenClass[cl] = true
		}
		for qid := range d.Queries {
			seenQuery[qid] = true
		}
		for _, v := range d.Verification {
			if v.QueryID != "" {
				seenQuery[v.QueryID] = true
			}
		}
	}
	for _, cl := range engine.Classes() {
		if seenClass[string(cl)] {
			m.Classes = append(m.Classes, string(cl))
			delete(seenClass, string(cl))
		}
	}
	// Any non-canonical labels (e.g. from a foreign schema) go last, sorted.
	extra := make([]string, 0, len(seenClass))
	for cl := range seenClass {
		extra = append(extra, cl)
	}
	sort.Strings(extra)
	m.Classes = append(m.Classes, extra...)
	for qid := range seenQuery {
		m.Queries = append(m.Queries, qid)
	}
	sort.Strings(m.Queries)
	return m
}

// verdict returns the verification record for a query id, or nil.
func (d *Document) verdict(qid string) *Verification {
	for i := range d.Verification {
		if d.Verification[i].QueryID == qid {
			return &d.Verification[i]
		}
	}
	return nil
}

// colLabel is the engine column-pair label: "engine/plane".
func colLabel(d *Document) string {
	return d.Condition.Engine + "/" + d.Condition.Plane
}

// --- units ---------------------------------------------------------------

// pickUnit selects the display unit for a set of latencies by the magnitude
// of their mean: ns under 1 µs, µs under 1 ms, ms under 1 s, s above. One
// unit per row keeps a row's pair of columns directly comparable.
func pickUnit(vals []time.Duration) string {
	if len(vals) == 0 {
		return "ms"
	}
	var total time.Duration
	for _, v := range vals {
		total += v
	}
	mean := total / time.Duration(len(vals))
	switch {
	case mean < time.Microsecond:
		return "ns"
	case mean < time.Millisecond:
		return "µs"
	case mean < time.Second:
		return "ms"
	default:
		return "s"
	}
}

// formatLatency renders d in the given unit, unit suffix included.
func formatLatency(d time.Duration, unit string) string {
	switch unit {
	case "ns":
		return fmt.Sprintf("%dns", d.Nanoseconds())
	case "µs":
		return fmt.Sprintf("%.1fµs", float64(d.Nanoseconds())/1e3)
	case "ms":
		return fmt.Sprintf("%.2fms", float64(d.Nanoseconds())/1e6)
	default:
		return fmt.Sprintf("%.3fs", d.Seconds())
	}
}

// --- row assembly --------------------------------------------------------

// matrixRow is one rendered row: a label and one (p50, p99) cell pair per
// engine column, already formatted.
type matrixRow struct {
	label string
	cells []string // 2*len(docs): p50, p99 per engine
}

// statFor returns the stat backing one (doc, row) cell, section-aware.
func statFor(d *Document, label string, query, cold bool) (ClassStat, bool) {
	if query {
		s, ok := d.Queries[label]
		return s, ok
	}
	if cold {
		s, ok := d.Cold[label]
		return s, ok
	}
	s, ok := d.Classes[label]
	return s, ok
}

// buildRow formats one row across all engine columns. Unit auto-selection is
// per row: the unit comes from the mean of the row's non-zero latencies, so
// a µs-scale point-read row and an s-scale analytical row each read in their
// natural unit. SKIP/FAIL verdicts render in the p50 cell of the pair.
func buildRow(docs []*Document, label string, query, cold bool) matrixRow {
	var vals []time.Duration
	for _, d := range docs {
		if s, ok := statFor(d, label, query, cold); ok {
			for _, v := range []time.Duration{s.P50, s.P99} {
				if v > 0 {
					vals = append(vals, v)
				}
			}
		}
	}
	unit := pickUnit(vals)
	row := matrixRow{label: label}
	for _, d := range docs {
		if query {
			if v := d.verdict(label); v != nil && v.Outcome != "PASS" {
				marker := v.Outcome
				if v.Outcome == "SKIP" && v.Reason != "" {
					marker = fmt.Sprintf("SKIP(%s)", v.Reason)
				}
				row.cells = append(row.cells, marker, "")
				continue
			}
		}
		s, ok := statFor(d, label, query, cold)
		if !ok {
			row.cells = append(row.cells, "n/a", "n/a")
			continue
		}
		// Every repetition errored. Errors are excluded from the percentile
		// slice, so P50 and P99 are the zero value here — and a latency
		// formatter turns that into "0.00ms", which reads as the fastest
		// engine in the row rather than the one that could not run the query
		// at all. Verification cannot catch this case: it runs one sample of
		// a write and passes, while the failure only appears from the second
		// repetition onward.
		if s.Errors > 0 && s.Errors == s.Count {
			row.cells = append(row.cells, fmt.Sprintf("ERR(%d/%d)", s.Errors, s.Count), "")
			continue
		}
		row.cells = append(row.cells, formatLatency(s.P50, unit), formatLatency(s.P99, unit))
	}
	return row
}

// buildRows assembles all rows in Interpretation order (spec 09 §3): class
// rollups first (warm, then cold rollups labelled "(cold)"), then per-query
// detail.
func buildRows(m *Matrix) []matrixRow {
	var rows []matrixRow
	for _, cl := range m.Classes {
		rows = append(rows, buildRow(m.Docs, cl, false, false))
	}
	for _, cl := range m.Classes {
		hasCold := false
		for _, d := range m.Docs {
			if _, ok := d.Cold[cl]; ok {
				hasCold = true
				break
			}
		}
		if hasCold {
			r := buildRow(m.Docs, cl, false, true)
			r.label = cl + " (cold)"
			rows = append(rows, r)
		}
	}
	for _, qid := range m.Queries {
		r := buildRow(m.Docs, qid, true, false)
		r.label = "  " + r.label // indent query detail under the rollups
		rows = append(rows, r)
	}
	return rows
}

// --- footers -------------------------------------------------------------

// writeFooters prints the fidelity line per workload (07 §6: every fidelity
// cell is restated in the report footer) and the condition line per engine
// (versions, dataset checksum, latency model, warmup outcome, tuned flag).
// prefix decorates each line ("" for the table, "_" pairs for markdown are
// handled by the caller passing prefix/suffix).
func writeFooters(w io.Writer, docs []*Document, prefix, suffix string) {
	seen := map[string]bool{}
	for _, d := range docs {
		wl := d.Workload
		if wl == "" {
			wl = d.Condition.Workload
		}
		if wl == "" || seen[wl] {
			continue
		}
		seen[wl] = true
		fid := d.Fidelity
		if fid == "" {
			fid = "unspecified"
		}
		fmt.Fprintf(w, "%sfidelity: %s: %s%s\n", prefix, wl, fid, suffix)
	}
	for _, d := range docs {
		c := d.Condition
		fmt.Fprintf(w, "%s%s: version %s, dataset %s, latency %s, warmup %s, tuned=%v%s\n",
			prefix, colLabel(d), orUnknown(c.EngineVersion), checksumPrefix8(c.DatasetChecksum),
			orUnknown(string(c.LatencyModel)), orUnknown(c.WarmupOutcome), c.Tuned, suffix)
	}
}

// writeErrors prints what the failing queries said, one line per engine and
// query that recorded an error. An ERR cell says a query did not run and
// nothing about why, and the samples are gone by the time the matrix is
// rendered, so the message the first failure carried is printed here.
//
// Partial failures get a line too. A query that fails one time in fifty still
// has percentiles, and they read as a healthy row, so the count and the
// message are the only sign anything went wrong.
func writeErrors(w io.Writer, docs []*Document, prefix, suffix string) {
	var lines []string
	for _, d := range docs {
		for _, qid := range slices.Sorted(maps.Keys(d.Queries)) {
			s := d.Queries[qid]
			if s.Errors == 0 || s.FirstError == "" {
				continue
			}
			lines = append(lines, fmt.Sprintf("%s%s %s: %d/%d failed, first: %s%s\n",
				prefix, colLabel(d), qid, s.Errors, s.Count, oneLine(s.FirstError), suffix))
		}
	}
	if len(lines) == 0 {
		return
	}
	for _, l := range lines {
		fmt.Fprint(w, l)
	}
	fmt.Fprintln(w)
}

// oneLine flattens an engine's error text so a multi-line diagnostic does not
// break the column the rest of the footer keeps, and caps it so a driver that
// dumps a whole query into its message does not bury the table.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return s
}

// orUnknown replaces an empty stamp field with "unknown" so an incomplete
// condition is visible instead of silently blank.
func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// --- table ---------------------------------------------------------------

// RenderTable writes the aligned plain-text matrix: a two-line header (engine
// labels over their p50/p99 column pair), class rollups, cold rollups,
// per-query detail, then the fidelity and condition footers.
func RenderTable(w io.Writer, m *Matrix) {
	rows := buildRows(m)

	labels := make([]string, len(m.Docs))
	for i, d := range m.Docs {
		labels[i] = colLabel(d)
	}

	// Column widths: label column plus 2 per engine.
	labelW := utf8.RuneCountInString("Class / Query")
	for _, r := range rows {
		labelW = max(labelW, utf8.RuneCountInString(r.label))
	}
	colW := make([]int, 2*len(m.Docs))
	for i := range colW {
		if i%2 == 0 {
			colW[i] = len("p50")
		} else {
			colW[i] = len("p99")
		}
	}
	for _, r := range rows {
		for i, c := range r.cells {
			colW[i] = max(colW[i], utf8.RuneCountInString(c))
		}
	}
	// Widen the pair to fit the engine label above it.
	for i := range m.Docs {
		pair := colW[2*i] + 2 + colW[2*i+1]
		if lw := utf8.RuneCountInString(labels[i]); lw > pair {
			colW[2*i] += lw - pair
		}
	}

	// Header line 1: engine labels spanning their pair.
	fmt.Fprint(w, pad("", labelW))
	for i, lab := range labels {
		fmt.Fprint(w, "  ", pad(lab, colW[2*i]+2+colW[2*i+1]))
	}
	fmt.Fprintln(w)
	// Header line 2: p50/p99 pairs.
	fmt.Fprint(w, pad("Class / Query", labelW))
	for i := range m.Docs {
		fmt.Fprint(w, "  ", pad("p50", colW[2*i]), "  ", pad("p99", colW[2*i+1]))
	}
	fmt.Fprintln(w)
	// Separator.
	fmt.Fprint(w, strings.Repeat("-", labelW))
	for _, cw := range colW {
		fmt.Fprint(w, "  ", strings.Repeat("-", cw))
	}
	fmt.Fprintln(w)

	for _, r := range rows {
		fmt.Fprint(w, pad(r.label, labelW))
		for i, c := range r.cells {
			fmt.Fprint(w, "  ", pad(c, colW[i]))
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w)
	writeErrors(w, m.Docs, "", "")
	writeFooters(w, m.Docs, "", "")
}

// pad left-aligns s in a field of width w, counting runes so "µs" cells stay
// aligned (byte-width padding drifts on multi-byte glyphs).
func pad(s string, w int) string {
	if n := utf8.RuneCountInString(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// --- markdown ------------------------------------------------------------

// RenderMarkdown writes the matrix as a GitHub-flavored Markdown table with
// the same row order as the plain-text table, footers as emphasized lines.
func RenderMarkdown(w io.Writer, m *Matrix) {
	rows := buildRows(m)

	var sb strings.Builder
	sb.WriteString("| Class / Query |")
	for _, d := range m.Docs {
		fmt.Fprintf(&sb, " %s p50 | p99 |", colLabel(d))
	}
	fmt.Fprintln(w, sb.String())
	sb.Reset()
	sb.WriteString("|---|")
	for range m.Docs {
		sb.WriteString("---|---|")
	}
	fmt.Fprintln(w, sb.String())

	for _, r := range rows {
		sb.Reset()
		fmt.Fprintf(&sb, "| %s |", strings.TrimSpace(r.label))
		for _, c := range r.cells {
			if c == "" {
				c = " "
			}
			fmt.Fprintf(&sb, " %s |", c)
		}
		fmt.Fprintln(w, sb.String())
	}

	fmt.Fprintln(w)
	writeFooters(w, m.Docs, "_", "_")
}

// --- csv -----------------------------------------------------------------

// RenderCSV writes one record per (engine, section, row) with every stat
// column explicit and durations in nanoseconds — the machine-diffable form
// the table and markdown views summarize.
func RenderCSV(w io.Writer, m *Matrix) error {
	cw := csv.NewWriter(w)
	header := []string{
		"engine", "plane", "section", "row", "outcome", "count", "errors",
		"min_ns", "p50_ns", "p90_ns", "p95_ns", "p99_ns", "max_ns",
		"mean_ns", "stddev_ns", "throughput", "row_throughput",
	}
	if err := cw.Write(header); err != nil {
		return err
	}
	write := func(d *Document, section, label, outcome string, s ClassStat) error {
		return cw.Write([]string{
			d.Condition.Engine, d.Condition.Plane, section, label, outcome,
			fmt.Sprintf("%d", s.Count), fmt.Sprintf("%d", s.Errors),
			fmt.Sprintf("%d", s.Min.Nanoseconds()), fmt.Sprintf("%d", s.P50.Nanoseconds()),
			fmt.Sprintf("%d", s.P90.Nanoseconds()), fmt.Sprintf("%d", s.P95.Nanoseconds()),
			fmt.Sprintf("%d", s.P99.Nanoseconds()), fmt.Sprintf("%d", s.Max.Nanoseconds()),
			fmt.Sprintf("%d", s.Mean.Nanoseconds()), fmt.Sprintf("%d", s.StdDev.Nanoseconds()),
			fmt.Sprintf("%.2f", s.Throughput), fmt.Sprintf("%.2f", s.RowThroughput),
		})
	}
	for _, d := range m.Docs {
		for _, cl := range m.Classes {
			if s, ok := d.Classes[cl]; ok {
				if err := write(d, "class", cl, "", s); err != nil {
					return err
				}
			}
			if s, ok := d.Cold[cl]; ok {
				if err := write(d, "cold", cl, "", s); err != nil {
					return err
				}
			}
		}
		for _, qid := range m.Queries {
			outcome := ""
			if v := d.verdict(qid); v != nil {
				outcome = v.Outcome
				if v.Outcome == "SKIP" && v.Reason != "" {
					outcome = fmt.Sprintf("SKIP(%s)", v.Reason)
				}
			}
			s, ok := d.Queries[qid]
			if !ok && outcome == "" {
				continue
			}
			if err := write(d, "query", qid, outcome, s); err != nil {
				return err
			}
		}
	}
	cw.Flush()
	return cw.Error()
}

// --- plane overhead ------------------------------------------------------

// RenderOverhead writes the plane-overhead table (spec 08 §8): for the given
// documents — normally one engine measured on two or more planes, or engines
// sharing a dialect and query set — the p50 per query per plane, with the
// absolute delta and the ratio against the fastest plane on that query. Only
// queries measured on at least two planes appear; the honest answer to "how
// much is the pipe" needs both ends of it.
func RenderOverhead(w io.Writer, docs []*Document) {
	ordered := NewMatrix(docs).Docs

	// Queries present in at least two documents.
	countByQ := map[string]int{}
	for _, d := range ordered {
		for qid := range d.Queries {
			countByQ[qid]++
		}
	}
	var queries []string
	for qid, n := range countByQ {
		if n >= 2 {
			queries = append(queries, qid)
		}
	}
	sort.Strings(queries)
	if len(queries) == 0 {
		fmt.Fprintln(w, "plane overhead: no query measured on more than one plane")
		return
	}

	header := []string{"Query"}
	for _, d := range ordered {
		header = append(header, colLabel(d)+" p50")
	}
	rows := [][]string{header}
	for _, qid := range queries {
		var vals []time.Duration
		fastest := time.Duration(0)
		for _, d := range ordered {
			if s, ok := d.Queries[qid]; ok && s.P50 > 0 {
				vals = append(vals, s.P50)
				if fastest == 0 || s.P50 < fastest {
					fastest = s.P50
				}
			}
		}
		unit := pickUnit(vals)
		row := []string{qid}
		for _, d := range ordered {
			s, ok := d.Queries[qid]
			if !ok || s.P50 == 0 {
				row = append(row, "n/a")
				continue
			}
			cell := formatLatency(s.P50, unit)
			if fastest > 0 && s.P50 > fastest {
				cell += fmt.Sprintf(" (+%s, x%.2f)",
					formatLatency(s.P50-fastest, unit), float64(s.P50)/float64(fastest))
			}
			row = append(row, cell)
		}
		rows = append(rows, row)
	}

	widths := make([]int, len(header))
	for _, r := range rows {
		for i, c := range r {
			widths[i] = max(widths[i], utf8.RuneCountInString(c))
		}
	}
	for ri, r := range rows {
		for i, c := range r {
			if i > 0 {
				fmt.Fprint(w, "  ")
			}
			fmt.Fprint(w, pad(c, widths[i]))
		}
		fmt.Fprintln(w)
		if ri == 0 {
			for i, cw := range widths {
				if i > 0 {
					fmt.Fprint(w, "  ")
				}
				fmt.Fprint(w, strings.Repeat("-", cw))
			}
			fmt.Fprintln(w)
		}
	}
}

// --- resources -----------------------------------------------------------

// resourceRow is one line of the resource table: the metric's name, how its
// values are formatted, and how to pull the value out of a document.
type resourceRow struct {
	label  string
	format func(int64) string
	value  func(measure.Resource) int64
}

// resourceRows is the resource table's shape, in reading order: memory first
// because it is the figure a reader compares engines on, then CPU, then the
// kernel work that explains a tail nothing in the query text does, then disk.
// A row whose value is -1 on every document is dropped, so a platform that
// cannot answer a metric prints a shorter table rather than a column of n/a.
var resourceRows = []resourceRow{
	{"peak rss", Bytes, func(r measure.Resource) int64 { return r.MaxRSSBytes }},
	{"peak rss (children)", Bytes, func(r measure.Resource) int64 { return r.ChildMaxRSSBytes }},
	{"heap live", Bytes, func(r measure.Resource) int64 { return r.HeapAllocBytes }},
	{"heap reserved", Bytes, func(r measure.Resource) int64 { return r.HeapSysBytes }},
	{"runtime total", Bytes, func(r measure.Resource) int64 { return r.GoSysBytes }},
	{"allocated", Bytes, func(r measure.Resource) int64 { return r.TotalAllocBytes }},
	{"gc cycles", countCell, func(r measure.Resource) int64 { return r.NumGC }},
	{"gc pause", Nanos, func(r measure.Resource) int64 { return r.GCPauseTotalNs }},
	{"cpu user", Nanos, func(r measure.Resource) int64 { return r.CPUUserNs }},
	{"cpu sys", Nanos, func(r measure.Resource) int64 { return r.CPUSysNs }},
	{"cpu user (child)", Nanos, func(r measure.Resource) int64 { return r.ChildCPUUserNs }},
	{"cpu sys (child)", Nanos, func(r measure.Resource) int64 { return r.ChildCPUSysNs }},
	{"minor faults", countCell, func(r measure.Resource) int64 { return r.MinorFaults }},
	{"major faults", countCell, func(r measure.Resource) int64 { return r.MajorFaults }},
	{"minor faults (children)", countCell, func(r measure.Resource) int64 { return r.ChildMinorFaults }},
	{"major faults (children)", countCell, func(r measure.Resource) int64 { return r.ChildMajorFaults }},
	{"ctx switches (vol)", countCell, func(r measure.Resource) int64 { return r.VoluntaryCtxSwitches }},
	{"ctx switches (invol)", countCell, func(r measure.Resource) int64 { return r.InvoluntaryCtxSwitches }},
	{"ctx switches (vol, children)", countCell, func(r measure.Resource) int64 { return r.ChildVoluntaryCtxSwitches }},
	{"ctx switches (invol, children)", countCell, func(r measure.Resource) int64 { return r.ChildInvoluntaryCtxSwitches }},
	{"block ops in", countCell, func(r measure.Resource) int64 { return r.BlockInputOps }},
	{"block ops out", countCell, func(r measure.Resource) int64 { return r.BlockOutputOps }},
	{"block ops in (children)", countCell, func(r measure.Resource) int64 { return r.ChildBlockInputOps }},
	{"block ops out (children)", countCell, func(r measure.Resource) int64 { return r.ChildBlockOutputOps }},
	{"disk read", Bytes, func(r measure.Resource) int64 { return r.DiskReadBytes }},
	{"disk write", Bytes, func(r measure.Resource) int64 { return r.DiskWriteBytes }},
	{"dataset on disk", Bytes, func(r measure.Resource) int64 { return r.DatasetBytes }},
	{"store after load", Bytes, func(r measure.Resource) int64 { return r.LoadBytes }},
	{"store after run", Bytes, func(r measure.Resource) int64 { return r.StoreBytes }},
	{"store growth", Bytes, func(r measure.Resource) int64 { return r.StoreGrowthBytes }},
}

// RenderResources writes the resource table (spec 08 §1 metrics 3/4 and the
// cost side of §7): one column per engine, one row per metric, over the same
// documents the latency matrix was built from. It is the answer to "at what
// price", which the latency table cannot give: two engines with the same p99
// are not equal if one of them burned four times the CPU or pushed ten times
// the bytes at the disk to get there.
//
// The scope of each figure is the harness process and the children it reaped,
// so an in-process engine reports itself, a subprocess engine reports itself in
// the child rows, and a Bolt engine reports its driver and nothing else. That
// is why the child rows are named rather than folded into the totals.
//
// Every counter row is a delta over the engine's own run. The two peak resident
// rows are not: the kernel keeps one high-water mark per process and does not
// reset it, so in an invocation that ran several engines a later column's peak
// includes what an earlier engine reached. The footer says so, and one engine
// per invocation is the way to attribute a peak.
func RenderResources(w io.Writer, docs []*Document) {
	ordered := NewMatrix(docs).Docs
	if len(ordered) == 0 {
		return
	}

	header := []string{"Resource"}
	for _, d := range ordered {
		header = append(header, colLabel(d))
	}
	rows := [][]string{header}
	for _, rr := range resourceRows {
		cells := make([]string, 0, len(ordered))
		any := false
		for _, d := range ordered {
			v := rr.value(d.Resource)
			if v != -1 {
				any = true
			}
			cells = append(cells, rr.format(v))
		}
		if !any {
			continue
		}
		rows = append(rows, append([]string{rr.label}, cells...))
	}
	if len(rows) == 1 {
		fmt.Fprintln(w, "resources: no document carries a resource capture")
		return
	}

	widths := make([]int, len(header))
	for _, r := range rows {
		for i, c := range r {
			widths[i] = max(widths[i], utf8.RuneCountInString(c))
		}
	}
	for ri, r := range rows {
		for i, c := range r {
			if i > 0 {
				fmt.Fprint(w, "  ")
			}
			fmt.Fprint(w, pad(c, widths[i]))
		}
		fmt.Fprintln(w)
		if ri == 0 {
			for i, cw := range widths {
				if i > 0 {
					fmt.Fprint(w, "  ")
				}
				fmt.Fprint(w, strings.Repeat("-", cw))
			}
			fmt.Fprintln(w)
		}
	}
	for _, line := range resourceNotes {
		fmt.Fprintln(w, line)
	}
}

// resourceNotes is the scope disclosure printed under the table. Without it a
// reader takes every row for the engine's own cost, and two of them are not:
// the peak resident rows are process high-water marks the kernel never resets,
// and the children rows hold whatever the harness forked, which is the engine
// on a subprocess plane and a load helper on an in-process one.
var resourceNotes = []string{
	"note: every counter row is a delta over that engine's own run.",
	"      the peak rss rows are process high-water marks the kernel never resets,",
	"      so run one engine per invocation to attribute a peak to it.",
	"      children rows are the engine itself on a subprocess plane, a load helper",
	"      on an in-process plane, and nothing on a Bolt plane: that server was not",
	"      forked here, so read the server process or its container to size it.",
}

// Bytes renders a byte count in the largest unit that keeps it readable,
// binary units because that is what an allocator and a page cache deal in. A
// negative count is the "nobody could ask" marker and prints as n/a, except
// that a store which shrank is a real measurement and keeps its sign.
func Bytes(v int64) string {
	if v == -1 {
		return "n/a"
	}
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	const unit = 1024
	if v < unit {
		return fmt.Sprintf("%s%d B", sign, v)
	}
	value := float64(v)
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%s%.1f %s", sign, value, suffix)
		}
	}
	return fmt.Sprintf("%s%.1f PiB", sign, value/unit)
}

// countCell renders a plain counter, n/a for the unavailable marker.
func countCell(v int64) string {
	if v == -1 {
		return "n/a"
	}
	return strconv.FormatInt(v, 10)
}

// Nanos renders a nanosecond figure as a duration, n/a for the unavailable
// marker. A zero is printed as a zero: a run that spent no measurable system
// time is a result, not a missing reading.
func Nanos(v int64) string {
	if v == -1 {
		return "n/a"
	}
	return time.Duration(v).String()
}

// HasTraversal says whether any of these documents carries a traversal rate,
// which is what a caller asks before spacing the section out.
func HasTraversal(docs []*Document) bool {
	for _, d := range docs {
		if len(d.TEPS) > 0 {
			return true
		}
	}
	return false
}

// RenderTraversal prints the TEPS section: one row per traversal kernel, the
// harmonic mean rate each engine reached, with the edge work the rate divides
// named beside the kernel. It prints nothing when no document carries a rate,
// which is every workload that has no kernel with a source.
//
// The rate is the number Graph500 headlines, and it is the honest way to
// compare a traversal across engines: a latency says how long one graph took,
// where a rate says how much graph went past per second, so two engines that
// were asked for different amounts of work are still comparable.
func RenderTraversal(w io.Writer, docs []*Document) {
	ordered := NewMatrix(docs).Docs
	kernels := map[string]int64{}
	for _, d := range ordered {
		for id, t := range d.TEPS {
			kernels[id] = t.Edges
		}
	}
	if len(kernels) == 0 {
		return
	}
	ids := make([]string, 0, len(kernels))
	for id := range kernels {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	header := []string{"Traversal rate"}
	for _, d := range ordered {
		header = append(header, colLabel(d))
	}
	rows := [][]string{header}
	for _, id := range ids {
		cells := []string{fmt.Sprintf("%s (%s edges)", id, Count(kernels[id]))}
		for _, d := range ordered {
			t, ok := d.TEPS[id]
			if !ok || t.HarmonicMean <= 0 {
				cells = append(cells, "n/a")
				continue
			}
			cells = append(cells, Rate(t.HarmonicMean))
		}
		rows = append(rows, cells)
	}

	widths := make([]int, len(header))
	for _, r := range rows {
		for i, c := range r {
			widths[i] = max(widths[i], utf8.RuneCountInString(c))
		}
	}
	for ri, r := range rows {
		for i, c := range r {
			if i > 0 {
				fmt.Fprint(w, "  ")
			}
			fmt.Fprint(w, pad(c, widths[i]))
		}
		fmt.Fprintln(w)
		if ri == 0 {
			for i, cw := range widths {
				if i > 0 {
					fmt.Fprint(w, "  ")
				}
				fmt.Fprint(w, strings.Repeat("-", cw))
			}
			fmt.Fprintln(w)
		}
	}
	fmt.Fprintln(w, "note: edges traversed per second, harmonic mean over the timed repetitions,")
	fmt.Fprintln(w, "      from the one source each kernel drew. an engine with no row for a")
	fmt.Fprintln(w, "      kernel did not run it.")
}

// Rate renders a per-second figure in engineering units, which is how a
// traversal rate is quoted and read.
func Rate(v float64) string {
	switch {
	case v >= 1e9:
		return fmt.Sprintf("%.2f G/s", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%.2f M/s", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%.2f K/s", v/1e3)
	default:
		return fmt.Sprintf("%.0f /s", v)
	}
}

// Count renders a plain count in the same engineering units, so the edge work
// beside a rate reads at the same glance.
func Count(v int64) string {
	switch {
	case v >= 1e9:
		return fmt.Sprintf("%.2fG", float64(v)/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%.2fM", float64(v)/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%.1fK", float64(v)/1e3)
	default:
		return strconv.FormatInt(v, 10)
	}
}
