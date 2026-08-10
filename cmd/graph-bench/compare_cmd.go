package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/tamnd/graph-bench/report"
)

// newCompareCmd builds the compare verb: N result documents side by side as
// one matrix, optionally with the plane-overhead section (spec 08 §8) — the
// honest answer to "how much is the pipe".
func newCompareCmd() *cobra.Command {
	var (
		files    []string
		dir      string
		wlFilter string
		scale    string
		latest   bool
		format   string
		outFile  string
		overhead bool
	)
	cmd := &cobra.Command{
		Use:   "compare",
		Short: "Compare recorded runs as a per-engine matrix",
		Long: "compare reads two or more JSON result documents (--files, comma-separated " +
			"or repeated) or a lineage directory (--results) and renders them side by " +
			"side. --overhead appends the plane-overhead table for engines that appear " +
			"on more than one plane.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var docs []*report.Document
			for _, path := range flattenEngines(files) {
				doc, err := report.Read(path)
				if err != nil {
					return fmt.Errorf("compare: %s: %w", path, err)
				}
				docs = append(docs, doc)
			}
			if len(docs) == 0 {
				var err error
				docs, err = readResults(dir, wlFilter, scale, "")
				if err != nil {
					return fmt.Errorf("compare: %w", err)
				}
				if latest {
					docs = latestPerEngine(docs)
				}
			}
			if len(docs) == 0 {
				return fmt.Errorf("compare: no results found; use --files or --results")
			}
			out := cmd.OutOrStdout()
			if outFile != "" {
				f, err := os.Create(outFile)
				if err != nil {
					return fmt.Errorf("compare: create %s: %w", outFile, err)
				}
				defer f.Close()
				out = f
			}
			if err := renderDocs(out, docs, format); err != nil {
				return err
			}
			if overhead {
				fmt.Fprintln(out)
				report.RenderOverhead(out, docs)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringSliceVar(&files, "files", nil, "JSON result documents to compare (comma-separated or repeated)")
	f.StringVar(&dir, "results", "results", "lineage directory to read when --files is empty")
	f.StringVar(&wlFilter, "workload", "", "filter lineage by workload name")
	f.StringVar(&scale, "scale", "", "filter lineage by scale label")
	f.BoolVar(&latest, "latest", true, "keep only the newest record per engine (lineage mode)")
	f.StringVar(&format, "format", "table", "output format: table|markdown|csv")
	f.StringVar(&outFile, "out", "", "output file (default: stdout)")
	f.BoolVar(&overhead, "overhead", false, "append the plane-overhead section")
	return cmd
}
