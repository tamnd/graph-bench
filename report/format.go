package report

import (
	"encoding/csv"
	"fmt"
	"io"
	"strings"
	"time"
)

// Render writes the matrix in the given format to w.
func Render(m *Matrix, format Format, w io.Writer) error {
	switch format {
	case FormatTable:
		return renderTable(m, w)
	case FormatMarkdown:
		return renderMarkdown(m, w)
	case FormatCSV:
		return renderCSV(m, w)
	default:
		return fmt.Errorf("unknown format %q", format)
	}
}

// renderTable writes an aligned terminal table. Columns pick their unit by
// magnitude of the median metric across all engines: µs for values under 1ms,
// ms for values under 1s, s otherwise.
func renderTable(m *Matrix, w io.Writer) error {
	if m.RunConditions != "" {
		fmt.Fprintf(w, "  %s\n\n", m.RunConditions)
	}

	units := columnUnits(m)

	// Build header.
	header := make([]string, 0, 2+len(m.Columns))
	header = append(header, "Engine", "Plane")
	for _, col := range m.Columns {
		u := units[col]
		header = append(header, fmt.Sprintf("%s (p99 %s)", col, u))
	}

	// Build data rows.
	rows := make([][]string, len(m.Rows))
	for i, r := range m.Rows {
		row := make([]string, 0, 2+len(m.Columns))
		row = append(row, r.Name, r.Plane)
		for _, col := range m.Columns {
			c := r.Cells[col]
			if c.Empty {
				row = append(row, "n/a")
				continue
			}
			row = append(row, formatLatency(c.Metric, units[col]))
		}
		rows[i] = row
	}

	// Compute column widths.
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	// Print header.
	printTableRow(w, header, widths)
	// Print separator.
	seps := make([]string, len(widths))
	for i, w := range widths {
		seps[i] = strings.Repeat("-", w)
	}
	printTableRow(w, seps, widths)

	// Print data rows.
	for _, row := range rows {
		printTableRow(w, row, widths)
	}
	return nil
}

func printTableRow(w io.Writer, cells []string, widths []int) {
	for i, cell := range cells {
		if i > 0 {
			fmt.Fprint(w, "  ")
		}
		fmt.Fprintf(w, "%-*s", widths[i], cell)
	}
	fmt.Fprintln(w)
}

// renderMarkdown writes a GitHub-flavored Markdown table. Identical structure
// to the terminal table but with pipe-delimited rows.
func renderMarkdown(m *Matrix, w io.Writer) error {
	if m.RunConditions != "" {
		fmt.Fprintf(w, "_Conditions: %s_\n\n", m.RunConditions)
	}

	units := columnUnits(m)

	// Header row.
	var sb strings.Builder
	sb.WriteString("| Engine | Plane |")
	for _, col := range m.Columns {
		u := units[col]
		fmt.Fprintf(&sb, " %s (p99 %s) |", col, u)
	}
	fmt.Fprintln(w, sb.String())

	// Separator row.
	sb.Reset()
	sb.WriteString("|--------|-------|")
	for range m.Columns {
		sb.WriteString("------------|")
	}
	fmt.Fprintln(w, sb.String())

	// Data rows.
	for _, r := range m.Rows {
		sb.Reset()
		fmt.Fprintf(&sb, "| %s | %s |", r.Name, r.Plane)
		for _, col := range m.Columns {
			c := r.Cells[col]
			if c.Empty {
				sb.WriteString(" n/a |")
			} else {
				fmt.Fprintf(&sb, " %s |", formatLatency(c.Metric, units[col]))
			}
		}
		fmt.Fprintln(w, sb.String())
	}
	return nil
}

// renderCSV writes one row per (engine, class) cell with explicit columns.
func renderCSV(m *Matrix, w io.Writer) error {
	cw := csv.NewWriter(w)
	header := append([]string{"engine", "plane", "version", "column", "p50", "p99", "throughput", "cold_p99"})
	if err := cw.Write(header); err != nil {
		return err
	}
	for _, r := range m.Rows {
		for _, col := range m.Columns {
			c := r.Cells[col]
			if c.Empty {
				continue
			}
			rec := []string{
				r.Name,
				r.Plane,
				r.Version,
				col,
				fmtDur(c.P50),
				fmtDur(c.Metric),
				fmt.Sprintf("%.2f", c.Throughput),
				fmtDur(c.Cold),
			}
			if err := cw.Write(rec); err != nil {
				return err
			}
		}
	}
	cw.Flush()
	return cw.Error()
}

// columnUnits picks the display unit for a column based on the median of the
// non-empty warm p99 values across all rows. Under 1 ms -> µs; under 1 s -> ms;
// else -> s.
func columnUnits(m *Matrix) map[string]string {
	units := make(map[string]string, len(m.Columns))
	for _, col := range m.Columns {
		var vals []time.Duration
		for _, r := range m.Rows {
			c := r.Cells[col]
			if !c.Empty && c.Metric > 0 {
				vals = append(vals, c.Metric)
			}
		}
		units[col] = pickUnit(vals)
	}
	return units
}

// pickUnit returns the display unit string for a set of latency values.
func pickUnit(vals []time.Duration) string {
	if len(vals) == 0 {
		return "ms"
	}
	var total time.Duration
	for _, v := range vals {
		total += v
	}
	median := total / time.Duration(len(vals))
	switch {
	case median < time.Millisecond:
		return "µs"
	case median < time.Second:
		return "ms"
	default:
		return "s"
	}
}

// formatLatency formats a duration using the given unit string.
func formatLatency(d time.Duration, unit string) string {
	switch unit {
	case "µs":
		return fmt.Sprintf("%.0f", float64(d.Nanoseconds())/1000)
	case "ms":
		return fmt.Sprintf("%.1f", float64(d.Nanoseconds())/1e6)
	default:
		return fmt.Sprintf("%.3f", d.Seconds())
	}
}

// fmtDur formats a duration as nanoseconds string; zero becomes empty string.
func fmtDur(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return fmt.Sprintf("%d", d.Nanoseconds())
}
