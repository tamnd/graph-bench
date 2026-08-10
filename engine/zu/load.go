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
// materialize the canonical rel files into one 2-column whitespace edge
// list and run `zu copy --reorder degree`. String ids map to themselves;
// zu copy keys them. LoadStats come from copy's own stats output, with
// liberal fallbacks (file size via os.Stat, wall-clock duration).
//
// Statements-only datasets need a statement executor: in shell or query
// mode each setup statement runs through Exec (Method "statements");
// in primitive mode Load fails with a clear error.
func (s *Session) Load(ctx context.Context, ds engine.Dataset) (engine.LoadStats, error) {
	if ds.Dir() == "" {
		return s.loadStatements(ctx, ds)
	}

	edgesPath := filepath.Join(s.workDir, "edges.txt")
	counted, err := materializeEdges(ds, edgesPath)
	if err != nil {
		return engine.LoadStats{}, err
	}

	start := time.Now()
	out, err := exec.CommandContext(ctx, s.bin,
		"copy", "--reorder", "degree", edgesPath, s.dbPath).CombinedOutput()
	if err != nil {
		return engine.LoadStats{}, fmt.Errorf("zu: copy failed: %v\n%s", err, out)
	}
	stats := parseCopyStats(string(out), s.dbPath, time.Since(start), counted)
	return stats, nil
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
