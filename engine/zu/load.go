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

// Load bulk-loads the dataset with zu's documented best practice (F2):
// materialize the canonical rel files into one edge list and run
// `zu copy --reorder degree`. String ids map to themselves; zu copy keys
// them. LoadStats come from copy's own stats output, with liberal
// fallbacks (file size via os.Stat, wall-clock duration).
//
// A dataset with one rel table that carries properties keeps them: the
// materialized file is a canonical CSV with its typed header, which zu
// copy reads as edge property columns, and a query can then read an
// edge's values. Anything else flattens to the 2-column whitespace list,
// because zu copy builds one edge table and addresses an edge property
// by the endpoint pair, so a second rel table has nowhere to go and a
// pair named twice has no one row to hold the values. That last one is
// only visible to copy, so a load that trips it retries flat and says so
// in Method rather than failing the run.
//
// Statements-only datasets need a statement executor: in shell or query
// mode each setup statement runs through Exec (Method "statements");
// in primitive mode Load fails with a clear error.
func (s *Session) Load(ctx context.Context, ds engine.Dataset) (engine.LoadStats, error) {
	if ds.Dir() == "" {
		return s.loadStatements(ctx, ds)
	}
	return copyDataset(ctx, "zu", s.bin, s.workDir, s.dbPath, ds)
}

// copyDataset is the whole bulk-load path, shared by both planes: the
// in-process adapter loads through the CLI too, so it has to make the same
// choice about edge properties or the two planes would not be measuring the
// same database. label prefixes the errors with the adapter's name.
func copyDataset(ctx context.Context, label, bin, workDir, dbPath string, ds engine.Dataset) (engine.LoadStats, error) {
	if typ, ok := propRel(ds); ok {
		edgesPath := filepath.Join(workDir, "edges.csv")
		counted, err := materializeProps(ds, typ, edgesPath)
		if err != nil {
			return engine.LoadStats{}, err
		}
		start := time.Now()
		out, err := exec.CommandContext(ctx, bin,
			"copy", "--reorder", "degree", edgesPath, dbPath).CombinedOutput()
		if err == nil {
			stats := parseCopyStats(string(out), dbPath, time.Since(start), counted)
			stats.Method = "copy (edge properties)"
			return stats, nil
		}
		propErr := fmt.Errorf("%s: copy with edge properties failed: %v\n%s", label, err, out)
		stats, flatErr := copyFlat(ctx, label, bin, workDir, dbPath, ds)
		if flatErr != nil {
			return engine.LoadStats{}, propErr
		}
		stats.Method = "copy (edge properties dropped)"
		return stats, nil
	}

	return copyFlat(ctx, label, bin, workDir, dbPath, ds)
}

// copyFlat is the 2-column whitespace path: every rel table's endpoints
// in one file, no properties.
func copyFlat(ctx context.Context, label, bin, workDir, dbPath string, ds engine.Dataset) (engine.LoadStats, error) {
	edgesPath := filepath.Join(workDir, "edges.txt")
	counted, err := materializeEdges(ds, edgesPath)
	if err != nil {
		return engine.LoadStats{}, err
	}

	start := time.Now()
	out, err := exec.CommandContext(ctx, bin,
		"copy", "--reorder", "degree", edgesPath, dbPath).CombinedOutput()
	if err != nil {
		return engine.LoadStats{}, fmt.Errorf("%s: copy failed: %v\n%s", label, err, out)
	}
	stats := parseCopyStats(string(out), dbPath, time.Since(start), counted)
	return stats, nil
}

// zuPropTypes are the header types zu copy reads as an edge property
// column. A dataset column of any other type keeps the flat path, since
// zu refuses a header type it has no column for rather than guessing.
var zuPropTypes = map[string]bool{
	"INT64": true, "FLOAT64": true, "BOOL": true, "STRING": true,
}

// propRel names the one rel table whose properties can travel with the
// edges, and reports whether there is one: exactly one rel table in the
// dataset, at least one property on it, and every property a type zu
// copy reads.
func propRel(ds engine.Dataset) (string, bool) {
	rels := ds.Schema().Rels
	if len(rels) != 1 {
		return "", false
	}
	for typ, rel := range rels {
		if len(rel.Properties) == 0 {
			return "", false
		}
		for _, col := range rel.Properties {
			if !zuPropTypes[strings.ToUpper(col.Type)] {
				return "", false
			}
		}
		return typ, true
	}
	return "", false
}

// loadStatements seeds a statements-only dataset by executing each setup
// statement, which requires a statement surface (shell or query mode).
func (s *Session) loadStatements(ctx context.Context, ds engine.Dataset) (engine.LoadStats, error) {
	if s.mode != modeShell && s.mode != modeQuery {
		return engine.LoadStats{}, fmt.Errorf(
			"zu: statements load requires zu query support (mode %q; zu has no query/shell verb yet)", s.mode)
	}
	start := time.Now()
	for i, stmt := range ds.Statements() {
		res, err := s.Exec(ctx, engine.Op{
			QueryID: fmt.Sprintf("load-statement-%d", i),
			Class:   engine.Write,
			Dialect: engine.ZuQL,
			Text:    stmt,
		})
		if err != nil {
			return engine.LoadStats{}, fmt.Errorf("zu: load statement %d: %w", i, err)
		}
		for res.Next() {
		}
		errStream := res.Err()
		res.Close()
		if errStream != nil {
			return engine.LoadStats{}, fmt.Errorf("zu: load statement %d: %w", i, errStream)
		}
	}
	bytes := int64(-1)
	if fi, err := os.Stat(s.dbPath); err == nil {
		bytes = fi.Size()
	}
	return engine.LoadStats{
		Duration:    time.Since(start),
		BytesOnDisk: bytes,
		Method:      "statements",
	}, nil
}

// materializeEdges concatenates every rel table's CSV files into one
// 2-column whitespace edge list at dst, skipping canonical headers
// (":START_ID,:END_ID,..."). Returns the number of edges written.
func materializeEdges(ds engine.Dataset, dst string) (int64, error) {
	rels := ds.Schema().Rels
	if len(rels) == 0 {
		return 0, fmt.Errorf("zu: dataset %q has no rel tables to load", ds.Name())
	}
	typs := make([]string, 0, len(rels))
	for t := range rels {
		typs = append(typs, t)
	}
	sort.Strings(typs)

	f, err := os.Create(dst)
	if err != nil {
		return 0, fmt.Errorf("zu: create edge list: %w", err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)

	var total int64
	for _, typ := range typs {
		files, err := ds.RelFiles(typ)
		if err != nil {
			return 0, fmt.Errorf("zu: rel files for %q: %w", typ, err)
		}
		for _, file := range files {
			n, err := appendEdges(w, file)
			if err != nil {
				return 0, fmt.Errorf("zu: reading %s: %w", file, err)
			}
			total += n
		}
	}
	if err := w.Flush(); err != nil {
		return 0, fmt.Errorf("zu: write edge list: %w", err)
	}
	return total, nil
}

// materializeProps concatenates one rel table's CSV files into a single
// canonical CSV at dst, header first and every file's own header row
// dropped, so zu copy sees one file with one typed header. Returns the
// number of edge rows written.
func materializeProps(ds engine.Dataset, typ, dst string) (int64, error) {
	files, err := ds.RelFiles(typ)
	if err != nil {
		return 0, fmt.Errorf("zu: rel files for %q: %w", typ, err)
	}
	if len(files) == 0 {
		return 0, fmt.Errorf("zu: rel table %q has no files", typ)
	}

	f, err := os.Create(dst)
	if err != nil {
		return 0, fmt.Errorf("zu: create edge csv: %w", err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)

	var total int64
	for i, file := range files {
		n, err := appendRows(w, file, i == 0)
		if err != nil {
			return 0, fmt.Errorf("zu: reading %s: %w", file, err)
		}
		total += n
	}
	if err := w.Flush(); err != nil {
		return 0, fmt.Errorf("zu: write edge csv: %w", err)
	}
	return total, nil
}

// appendRows copies one rel CSV through verbatim, keeping its header
// only for the first file of the table. Returns the data rows written.
func appendRows(w *bufio.Writer, path string, keepHeader bool) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	r := bufio.NewScanner(f)
	r.Buffer(make([]byte, 0, 64<<10), 16<<20)
	var n int64
	first := true
	for r.Scan() {
		line := r.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		if first {
			first = false
			if isHeaderRow(strings.Split(line, ",")) {
				if !keepHeader {
					continue
				}
				if _, err := w.WriteString(line + "\n"); err != nil {
					return n, err
				}
				continue
			}
			return n, fmt.Errorf("no header row, so the property columns have no names")
		}
		if _, err := w.WriteString(line + "\n"); err != nil {
			return n, err
		}
		n++
	}
	return n, r.Err()
}

// appendEdges writes "src dst" lines from one rel CSV's first two
// columns, skipping a leading header row when present.
func appendEdges(w *bufio.Writer, path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.ReuseRecord = true

	var n int64
	first := true
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, err
		}
		if len(rec) < 2 {
			return n, fmt.Errorf("row %d has %d columns, need at least 2 (src, dst)", n+1, len(rec))
		}
		if first {
			first = false
			if isHeaderRow(rec) {
				continue
			}
		}
		if _, err := fmt.Fprintf(w, "%s %s\n", rec[0], rec[1]); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// isHeaderRow recognizes the canonical layout's rel headers
// (":START_ID,:END_ID,:TYPE") and common plain-name variants.
func isHeaderRow(rec []string) bool {
	names := map[string]bool{
		"src": true, "dst": true, "source": true, "target": true,
		"start": true, "end": true, "from": true, "to": true,
	}
	for _, field := range rec[:2] {
		f := strings.TrimSpace(field)
		if strings.HasPrefix(f, ":") || strings.Contains(f, ":") {
			return true
		}
		if names[strings.ToLower(f)] {
			return true
		}
	}
	return false
}

// Stats-line patterns from zu copy's output, verified against
// crates/zu-cli/src/main.rs 2026-08-10:
//
//	copied 1234 edges, 567 nodes, 8 groups
//	parse 0.10s, encode+write 0.15s, total 0.25s
//	1.25 M edges/s end to end, 4096 bytes on disk, 12.50 bits/edge fwd, ...
var (
	reCopied      = regexp.MustCompile(`copied\s+([0-9]+)\s+edges,\s+([0-9]+)\s+nodes`)
	reEdges       = regexp.MustCompile(`\b([0-9]+)\s+edges\b`)
	reNodes       = regexp.MustCompile(`\b([0-9]+)\s+nodes\b`)
	reTotalSecs   = regexp.MustCompile(`total\s+([0-9.]+)\s*s\b`)
	reMEdgesPerS  = regexp.MustCompile(`([0-9.]+)\s+M edges/s`)
	reBytesOnDisk = regexp.MustCompile(`([0-9]+)\s+bytes on disk`)
	reBytes       = regexp.MustCompile(`\b([0-9]+)\s+bytes\b`)
)

// parseCopyStats extracts LoadStats from zu copy's stdout, liberally:
// every value has a fallback (counted edges from materialization, wall
// clock, os.Stat on the produced file) so a format drift degrades to
// coarser numbers instead of failing the load.
func parseCopyStats(out, dbPath string, wall time.Duration, countedEdges int64) engine.LoadStats {
	stats := engine.LoadStats{
		Duration:    wall,
		Edges:       countedEdges,
		BytesOnDisk: -1,
		Method:      "copy",
	}

	if m := reCopied.FindStringSubmatch(out); m != nil {
		stats.Edges = atoi64(m[1], stats.Edges)
		stats.Nodes = atoi64(m[2], 0)
	} else {
		if m := reEdges.FindStringSubmatch(out); m != nil {
			stats.Edges = atoi64(m[1], stats.Edges)
		}
		if m := reNodes.FindStringSubmatch(out); m != nil {
			stats.Nodes = atoi64(m[1], 0)
		}
	}

	if m := reTotalSecs.FindStringSubmatch(out); m != nil {
		if secs, err := strconv.ParseFloat(m[1], 64); err == nil && secs > 0 {
			stats.Duration = time.Duration(secs * float64(time.Second))
		}
	} else if m := reMEdgesPerS.FindStringSubmatch(out); m != nil && stats.Edges > 0 {
		if rate, err := strconv.ParseFloat(m[1], 64); err == nil && rate > 0 {
			secs := float64(stats.Edges) / (rate * 1e6)
			stats.Duration = time.Duration(secs * float64(time.Second))
		}
	}

	if m := reBytesOnDisk.FindStringSubmatch(out); m != nil {
		stats.BytesOnDisk = atoi64(m[1], -1)
	} else if m := reBytes.FindStringSubmatch(out); m != nil {
		stats.BytesOnDisk = atoi64(m[1], -1)
	} else if fi, err := os.Stat(dbPath); err == nil {
		stats.BytesOnDisk = fi.Size()
	}

	return stats
}

func atoi64(s string, def int64) int64 {
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v
	}
	return def
}
