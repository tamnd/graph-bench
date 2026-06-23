package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/tamnd/graph-bench/report"
)

// newCompareCmd builds the compare verb. It reads two or more JSON results
// files or lineage records and assembles them side-by-side into one matrix.
// The primary use case is comparing across engines (gr vs Neo4j vs Memgraph)
// or across commits (before vs after a code change). See doc 08 section 1.3.
func newCompareCmd() *cobra.Command {
	var (
		files      []string
		lineage    string
		workload   string
		scale      string
		latest     bool
		format     string
		outFile    string
		mode       string
		showDelta  bool
		inprocName string
		boltName   string
	)

	cmd := &cobra.Command{
		Use:   "compare",
		Short: "Compare recorded runs as a per-engine matrix",
		Long: "compare reads two or more JSON results files (--files) or a lineage tree " +
			"(--lineage) and renders them as a comparison matrix. " +
			"--files takes a comma-separated list of JSON files (from 'run --format json'). " +
			"When reading from the lineage, --workload and --scale filter the records " +
			"and --latest keeps only the newest record per engine. " +
			"--overhead adds an extra section with the Bolt protocol overhead relative " +
			"to the in-process baseline (requires --inproc and --bolt).",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve format.
			var outFmt report.Format
			switch format {
			case "table", "":
				outFmt = report.FormatTable
			case "json":
				outFmt = report.FormatJSON
			case "markdown", "md":
				outFmt = report.FormatMarkdown
			case "csv":
				outFmt = report.FormatCSV
			default:
				return fmt.Errorf("compare: --format must be table|json|markdown|csv, got %q", format)
			}

			// Resolve column mode.
			var colMode report.ColumnMode
			switch mode {
			case "class", "":
				colMode = report.ColumnClass
			case "query":
				colMode = report.ColumnQuery
			default:
				return fmt.Errorf("compare: --mode must be class|query, got %q", mode)
			}

			// Load results from --files.
			var results []report.EngineResult
			for _, path := range files {
				f, err := os.Open(path)
				if err != nil {
					return fmt.Errorf("compare: open %s: %w", path, err)
				}
				r, parseErr := report.ParseJSON(f)
				f.Close()
				if parseErr != nil {
					return fmt.Errorf("compare: parse %s: %w", path, parseErr)
				}
				results = append(results, r...)
			}

			// Load results from --lineage (additive with --files).
			if lineage != "" || len(files) == 0 {
				base := lineage
				if base == "" {
					base = "results"
				}
				r, err := report.ReadLineage(base, workload, scale, "")
				if err != nil {
					return fmt.Errorf("compare: lineage: %w", err)
				}
				results = append(results, r...)
			}

			if len(results) == 0 {
				return fmt.Errorf("compare: no results found; use --files or --lineage")
			}

			if latest {
				results = latestPerEngine(results)
			}

			// Open output writer.
			out := cmd.OutOrStdout()
			if outFile != "" {
				f, err := os.Create(outFile)
				if err != nil {
					return fmt.Errorf("compare: create %s: %w", outFile, err)
				}
				defer f.Close()
				out = f
			}

			if outFmt == report.FormatJSON {
				return report.RenderJSON(results, out)
			}

			conditions := conditionSummary(results)
			m := report.Assemble(results, colMode, conditions)

			if err := report.Render(m, outFmt, out); err != nil {
				return err
			}

			// Optional plane-overhead section.
			if showDelta && inprocName != "" && boltName != "" {
				ov := m.PlaneOverhead(inprocName, boltName)
				if ov == nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "compare: overhead: engines %q and %q not both present in results\n", inprocName, boltName)
				} else {
					fmt.Fprintln(out, "")
					fmt.Fprintf(out, "Bolt plane overhead: %s (inproc) vs %s (bolt)\n", ov.InprocEngine, ov.BoltEngine)
					fmt.Fprintf(out, "%-14s  %-12s  %s\n", "column", "absolute", "relative")
					fmt.Fprintf(out, "%-14s  %-12s  %s\n", "------", "--------", "--------")
					for _, col := range m.Columns {
						o, ok := ov.Overhead[col]
						if !ok {
							continue
						}
						fmt.Fprintf(out, "%-14s  %-12s  %.2fx\n",
							col, o.Absolute.String(), o.Relative)
					}
				}
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringSliceVar(&files, "files", nil, "comma-separated JSON results files to compare")
	f.StringVar(&lineage, "lineage", "", "lineage tree root (default: results/)")
	f.StringVar(&workload, "workload", "", "filter lineage by workload name")
	f.StringVar(&scale, "scale", "", "filter lineage by scale factor")
	f.BoolVar(&latest, "latest", false, "keep only the newest record per engine")
	f.StringVar(&format, "format", "table", "output format: table|json|markdown|csv")
	f.StringVar(&outFile, "out", "", "output file (default: stdout)")
	f.StringVar(&mode, "mode", "class", "column mode: class|query")
	f.BoolVar(&showDelta, "overhead", false, "print Bolt plane overhead section")
	f.StringVar(&inprocName, "inproc", "gr", "in-process engine name for overhead computation")
	f.StringVar(&boltName, "bolt", "gr-bolt", "Bolt engine name for overhead computation")
	return cmd
}
