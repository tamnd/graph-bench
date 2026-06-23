package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/tamnd/graph-bench/report"
)

// newReportCmd re-renders a previously-recorded JSON result or a lineage tree
// in any of the four output formats. It reads from a file (--file) or from the
// lineage tree (--lineage), with optional filters for workload, scale, and
// engine. The --latest flag keeps only the newest record per engine when reading
// from the lineage. See doc 08 section 1.2 for the verb's contract.
func newReportCmd() *cobra.Command {
	var (
		inFile   string
		lineage  string
		workload string
		scale    string
		engine   string
		latest   bool
		format   string
		outFile  string
		mode     string
	)

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Render a recorded run or comparison in a chosen format",
		Long: "report reads a JSON results file (--file) or a lineage tree (--lineage) " +
			"and renders it as a terminal table, Markdown, CSV, or JSON. " +
			"When reading from the lineage you can filter by --workload, --scale, and --engine, " +
			"and --latest keeps only the newest record per engine. " +
			"--mode selects between class-level columns (class) and per-query columns (query).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Resolve the column mode.
			var colMode report.ColumnMode
			switch mode {
			case "class", "":
				colMode = report.ColumnClass
			case "query":
				colMode = report.ColumnQuery
			default:
				return fmt.Errorf("report: --mode must be 'class' or 'query', got %q", mode)
			}

			// Resolve the output format.
			var fmt_ report.Format
			switch format {
			case "table", "":
				fmt_ = report.FormatTable
			case "json":
				fmt_ = report.FormatJSON
			case "markdown", "md":
				fmt_ = report.FormatMarkdown
			case "csv":
				fmt_ = report.FormatCSV
			default:
				return fmt.Errorf("report: --format must be table|json|markdown|csv, got %q", format)
			}

			// Load the results.
			var results []report.EngineResult
			var loadErr error
			if inFile != "" {
				f, err := os.Open(inFile)
				if err != nil {
					return fmt.Errorf("report: open %s: %w", inFile, err)
				}
				defer f.Close()
				results, loadErr = report.ParseJSON(f)
			} else {
				base := lineage
				if base == "" {
					base = "results"
				}
				results, loadErr = report.ReadLineage(base, workload, scale, engine)
			}
			if loadErr != nil {
				return fmt.Errorf("report: load results: %w", loadErr)
			}
			if len(results) == 0 {
				return fmt.Errorf("report: no results matched the given filters")
			}

			// Apply --latest: keep only the newest record per engine.
			if latest {
				results = latestPerEngine(results)
			}

			// Open the output writer.
			out := cmd.OutOrStdout()
			if outFile != "" {
				f, err := os.Create(outFile)
				if err != nil {
					return fmt.Errorf("report: create %s: %w", outFile, err)
				}
				defer f.Close()
				out = f
			}

			// JSON is a special path that skips the matrix assembly.
			if fmt_ == report.FormatJSON {
				return report.RenderJSON(results, out)
			}

			// Build run conditions string from the first result's condition.
			conditions := conditionSummary(results)

			// Assemble and render.
			m := report.Assemble(results, colMode, conditions)
			return report.Render(m, fmt_, out)
		},
	}

	f := cmd.Flags()
	f.StringVar(&inFile, "file", "", "JSON results file to render (from 'run --format json')")
	f.StringVar(&lineage, "lineage", "", "lineage tree root (default: results/)")
	f.StringVar(&workload, "workload", "", "filter by workload name")
	f.StringVar(&scale, "scale", "", "filter by scale factor")
	f.StringVar(&engine, "engine", "", "filter by engine name")
	f.BoolVar(&latest, "latest", false, "keep only the newest record per engine")
	f.StringVar(&format, "format", "table", "output format: table|json|markdown|csv")
	f.StringVar(&outFile, "out", "", "output file (default: stdout)")
	f.StringVar(&mode, "mode", "class", "column mode: class|query")
	return cmd
}

// latestPerEngine returns one result per engine, keeping the one with the
// latest Condition.Timestamp. When timestamps are equal (or zero) the last
// one in slice order wins.
func latestPerEngine(results []report.EngineResult) []report.EngineResult {
	seen := map[string]int{} // engine name -> index in out
	var out []report.EngineResult
	for _, r := range results {
		name := r.Name
		if idx, ok := seen[name]; !ok {
			seen[name] = len(out)
			out = append(out, r)
		} else {
			if !r.Result.Condition.Timestamp.Before(out[idx].Result.Condition.Timestamp) {
				out[idx] = r
			}
		}
	}
	return out
}

// conditionSummary builds a one-line human-readable conditions string from the
// first result's Condition stamp. Used in table and Markdown headers.
func conditionSummary(results []report.EngineResult) string {
	if len(results) == 0 {
		return ""
	}
	c := results[0].Result.Condition
	s := ""
	if c.Workload != "" {
		s += "workload=" + c.Workload
	}
	if c.Dataset != "" {
		if s != "" {
			s += " "
		}
		s += "dataset=" + c.Dataset
	}
	if c.Scale != "" {
		if s != "" {
			s += " "
		}
		s += "scale=" + c.Scale
	}
	if c.Hardware != "" {
		if s != "" {
			s += " "
		}
		s += "hw=" + c.Hardware
	}
	return s
}
