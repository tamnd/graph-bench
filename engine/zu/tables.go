package zu

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// copyTables is the load that keeps the tables the dataset was written
// with: one zu node table per node label, one zu rel table per rel type,
// each rel table bound to the two node tables its manifest names as its
// ends. `zu copy --node Label=file --rel Type=Start:End:file out.zu1`.
//
// The flat path below it exists because zu used to build exactly one
// node table and one edge table, so a dataset of three labels and six
// rel types went in as a bare edge list and every query that reads a
// transfer's amount had nothing to read. That is why the finbench reads
// SKIP on this engine. This is the load that answers them.
//
// It is guarded rather than assumed. A dataset whose manifest does not
// name both ends of every rel type, or whose ids are not integers, or
// whose node labels are not all endpoints, is not one zu can take this
// way, and the caller falls back to the flat load rather than failing
// the run.
func copyTables(ctx context.Context, label, bin, workDir, dbPath string, ds engine.Dataset) (engine.LoadStats, error) {
	schema := ds.Schema()
	labels, typs, err := tableNames(schema)
	if err != nil {
		return engine.LoadStats{}, err
	}

	dir := filepath.Join(workDir, "tables")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return engine.LoadStats{}, fmt.Errorf("zu: create table dir: %w", err)
	}
	args := []string{"copy"}
	var dropped []string
	for _, name := range labels {
		path := filepath.Join(dir, "node-"+name+".csv")
		files, err := ds.NodeFiles(name)
		if err != nil {
			return engine.LoadStats{}, fmt.Errorf("zu: node files for %q: %w", name, err)
		}
		skipped, _, err := materializeTable(files, path)
		if err != nil {
			return engine.LoadStats{}, fmt.Errorf("zu: node table %q: %w", name, err)
		}
		dropped = append(dropped, skipped...)
		args = append(args, "--node", name+"="+path)
	}
	var counted int64
	for _, typ := range typs {
		path := filepath.Join(dir, "rel-"+typ+".csv")
		files, err := ds.RelFiles(typ)
		if err != nil {
			return engine.LoadStats{}, fmt.Errorf("zu: rel files for %q: %w", typ, err)
		}
		skipped, rows, err := materializeTable(files, path)
		if err != nil {
			return engine.LoadStats{}, fmt.Errorf("zu: rel table %q: %w", typ, err)
		}
		dropped = append(dropped, skipped...)
		counted += rows
		rel := schema.Rels[typ]
		args = append(args, "--rel", typ+"="+rel.Start+":"+rel.End+":"+path)
	}
	args = append(args, dbPath)

	start := time.Now()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		return engine.LoadStats{}, fmt.Errorf("%s: copy of tables failed: %v\n%s", label, err, out)
	}
	stats := parseTableStats(string(out), dbPath, time.Since(start), counted)
	stats.Method = "copy (tables)"
	if len(dropped) > 0 {
		sort.Strings(dropped)
		stats.Method = "copy (tables, dropped " + strings.Join(dedupe(dropped), ", ") + ")"
	}
	return stats, nil
}

// tableNames reports the node labels and rel types of a dataset zu can
// load table by table, or says why it cannot.
//
// Every rel type has to name both of its ends, because a zu rel table is
// bound to two node tables and there is nothing to bind it to otherwise.
// Every node label has to be an end of some rel type, because a zu node
// table gets its row domain from the loads that touch it. Both are true
// of every canonical dataset a manifest was written for; a hand-made
// directory without one is what this refuses.
func tableNames(schema engine.Schema) ([]string, []string, error) {
	if len(schema.Nodes) == 0 || len(schema.Rels) == 0 {
		return nil, nil, fmt.Errorf("zu: a table load needs at least one node table and one rel table")
	}
	labels := make([]string, 0, len(schema.Nodes))
	for name := range schema.Nodes {
		labels = append(labels, name)
	}
	sort.Strings(labels)
	typs := make([]string, 0, len(schema.Rels))
	for typ := range schema.Rels {
		typs = append(typs, typ)
	}
	sort.Strings(typs)

	touched := make(map[string]bool, len(labels))
	for _, typ := range typs {
		rel := schema.Rels[typ]
		for _, end := range []string{rel.Start, rel.End} {
			if end == "" {
				return nil, nil, fmt.Errorf("zu: rel table %q does not name both of its ends", typ)
			}
			if _, ok := schema.Nodes[end]; !ok {
				return nil, nil, fmt.Errorf("zu: rel table %q names the end %q, which is no node table", typ, end)
			}
			touched[end] = true
		}
	}
	for _, name := range labels {
		if !touched[name] {
			return nil, nil, fmt.Errorf("zu: node table %q is an end of no rel table", name)
		}
	}
	return labels, typs, nil
}

// zuHeaderTypes are the header types zu reads. ID, START_ID, END_ID and
// TYPE are structure; the four scalars are property columns. Anything
// else is a column zu has no lane for, and it is dropped from the
// materialized file rather than failing the load, since a dataset is
// worth loading for the columns it has that zu can hold.
var zuHeaderTypes = map[string]bool{
	"ID": true, "START_ID": true, "END_ID": true, "TYPE": true,
	"INT64": true, "FLOAT64": true, "BOOL": true, "STRING": true,
}

// materializeTable concatenates one table's CSV files into a single
// canonical CSV at dst, header first and every later file's own header
// dropped, keeping only the columns zu has a lane for. Returns the names
// of the columns it dropped and the number of data rows written.
//
// zu copy takes one file per table and the canonical layout may split a
// table across several, so this is a concatenation and not a copy. The
// column filter is the other half: zu refuses a header type it has no
// column for, and one DATETIME in a node file would otherwise cost the
// whole table.
func materializeTable(files []string, dst string) ([]string, int64, error) {
	if len(files) == 0 {
		return nil, 0, fmt.Errorf("no files")
	}
	out, err := os.Create(dst)
	if err != nil {
		return nil, 0, fmt.Errorf("create %s: %w", dst, err)
	}
	defer out.Close()
	w := bufio.NewWriter(out)

	var (
		keep    []int
		dropped []string
		rows    int64
	)
	for i, file := range files {
		n, err := appendTableRows(w, file, i == 0, &keep, &dropped)
		if err != nil {
			return nil, 0, fmt.Errorf("reading %s: %w", file, err)
		}
		rows += n
	}
	if err := w.Flush(); err != nil {
		return nil, 0, fmt.Errorf("write %s: %w", dst, err)
	}
	return dropped, rows, nil
}

// appendTableRows copies one CSV through, narrowed to the columns zu
// reads. The first file decides which those are and its header is the
// one written; a later file is assumed to agree, which the canonical
// layout guarantees since one table's files share one header.
func appendTableRows(w *bufio.Writer, path string, first bool, keep *[]int, dropped *[]string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1

	head, err := r.Read()
	if err == io.EOF {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if first {
		for i, cell := range head {
			name, rest, tagged := strings.Cut(strings.TrimSpace(cell), ":")
			if !tagged || !zuHeaderTypes[strings.ToUpper(strings.TrimSpace(rest))] {
				if name == "" {
					name = strings.TrimSpace(cell)
				}
				*dropped = append(*dropped, name)
				continue
			}
			*keep = append(*keep, i)
		}
		if err := writeCells(w, head, *keep); err != nil {
			return 0, err
		}
	}

	var n int64
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		if err := writeCells(w, rec, *keep); err != nil {
			return n, err
		}
		n++
	}
}

// writeCells writes the kept columns of one record as a CSV line. A
// value with a comma in it would break the line, and zu splits on commas
// and nothing else, so it is refused here where the column and the file
// are still known.
func writeCells(w *bufio.Writer, rec []string, keep []int) error {
	for i, col := range keep {
		if col >= len(rec) {
			return fmt.Errorf("a row has %d fields where the header names %d", len(rec), col+1)
		}
		cell := strings.TrimSpace(rec[col])
		if strings.ContainsAny(cell, ",\n") {
			return fmt.Errorf("the value %q holds a comma, which zu reads as a column break", cell)
		}
		if i > 0 {
			if err := w.WriteByte(','); err != nil {
				return err
			}
		}
		if _, err := w.WriteString(cell); err != nil {
			return err
		}
	}
	return w.WriteByte('\n')
}

// dedupe returns the sorted input with runs of equal strings collapsed.
func dedupe(sorted []string) []string {
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// Stats-line patterns from the table form of zu copy's output, verified
// against crates/zu-cli/src/main.rs 2026-08-18:
//
//	copied 1234 edges, 567 nodes, 2 node table(s), 6 rel table(s)
//	total 0.25s, 1.25 M edges/s end to end, 4096 bytes on disk
var (
	reTableCopied = regexp.MustCompile(`copied\s+([0-9]+)\s+edges,\s+([0-9]+)\s+nodes`)
	reTableTotal  = regexp.MustCompile(`total\s+([0-9.]+)s`)
	reTableBytes  = regexp.MustCompile(`([0-9]+)\s+bytes on disk`)
)

// parseTableStats extracts LoadStats from the table copy's stdout, with
// the same liberality parseCopyStats has: every value has a fallback, so
// a format drift degrades to coarser numbers instead of failing a load
// that worked.
func parseTableStats(out, dbPath string, wall time.Duration, countedEdges int64) engine.LoadStats {
	stats := engine.LoadStats{
		Duration:    wall,
		Edges:       countedEdges,
		BytesOnDisk: -1,
		Method:      "copy (tables)",
	}
	if m := reTableCopied.FindStringSubmatch(out); m != nil {
		stats.Edges = atoi64(m[1], stats.Edges)
		stats.Nodes = atoi64(m[2], 0)
	}
	if m := reTableTotal.FindStringSubmatch(out); m != nil {
		if secs, err := strconv.ParseFloat(m[1], 64); err == nil && secs > 0 {
			stats.Duration = time.Duration(secs * float64(time.Second))
		}
	}
	if m := reTableBytes.FindStringSubmatch(out); m != nil {
		stats.BytesOnDisk = atoi64(m[1], -1)
	} else if fi, err := os.Stat(dbPath); err == nil {
		stats.BytesOnDisk = fi.Size()
	}
	return stats
}
