package report

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// RenderResources writes a per-engine resource summary beneath the latency
// matrix: load time, on-disk sizes, and the memory the run cost. The matrix
// answers how fast; this answers at what price. It is skipped for the JSON
// format, which already carries the Resource struct losslessly.
//
// The memory columns describe the harness process, so they read true for
// in-process engines and near-zero for Bolt engines whose work runs in a
// separate server; the heading note says so rather than letting a 4 MB client
// footprint read as the engine's memory.
func RenderResources(results []EngineResult, w io.Writer) error {
	header := []string{"Engine", "Plane", "Load", "Nodes", "Edges", "Dataset", "OnDisk", "Heap", "MaxRSS", "Alloc'd", "GC"}
	rows := make([][]string, 0, len(results))
	anyBolt := false
	for _, er := range results {
		if er.Plane != "inproc" {
			anyBolt = true
		}
		ld := er.Result.Load
		rc := er.Result.Resource
		rows = append(rows, []string{
			er.Name,
			er.Plane,
			shortDur(ld.Duration),
			count(ld.Nodes),
			count(ld.Edges),
			bytesIEC(rc.DatasetBytes),
			bytesIEC(rc.LoadBytes),
			bytesIEC(rc.HeapAllocBytes),
			bytesIEC(rc.MaxRSSBytes),
			bytesIEC(rc.TotalAllocBytes),
			fmt.Sprintf("%d", rc.NumGC),
		})
	}

	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = len(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if len(c) > widths[i] {
				widths[i] = len(c)
			}
		}
	}

	fmt.Fprintln(w, "  Resources")
	writeRow(w, header, widths)
	seps := make([]string, len(widths))
	for i, ww := range widths {
		seps[i] = strings.Repeat("-", ww)
	}
	writeRow(w, seps, widths)
	for _, r := range rows {
		writeRow(w, r, widths)
	}
	if anyBolt {
		fmt.Fprintln(w, "  note: Heap/MaxRSS/Alloc'd/GC are the harness process; for Bolt engines they cover only the client, not the server.")
	}
	return nil
}

func writeRow(w io.Writer, cells []string, widths []int) {
	for i, c := range cells {
		if i > 0 {
			fmt.Fprint(w, "  ")
		}
		fmt.Fprintf(w, "%-*s", widths[i], c)
	}
	fmt.Fprintln(w)
}

// bytesIEC formats a byte count in binary units. A negative value is "n/a"
// (the convention for an unknown or not-applicable size, e.g. an in-memory
// engine's on-disk footprint).
func bytesIEC(b int64) string {
	if b < 0 {
		return "n/a"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// count formats a node/edge count with thousands separators; -1 reads "n/a".
func count(n int64) string {
	if n < 0 {
		return "n/a"
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// shortDur formats a load duration compactly; zero reads "n/a".
func shortDur(d time.Duration) string {
	if d <= 0 {
		return "n/a"
	}
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	default:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
}
