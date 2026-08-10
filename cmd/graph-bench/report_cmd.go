package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tamnd/graph-bench/report"
)

// newReportCmd builds the report verb: it re-renders stored schema-3 result
// documents (or a single file) as a comparison matrix in table, markdown, or
// CSV form. Readers accept schema-2 (v1) records for continuity (spec 09 §2).
func newReportCmd() *cobra.Command {
	var (
		inFile   string
		dir      string
		wlFilter string
		scale    string
		engineF  string
		latest   bool
		format   string
		outFile  string
	)
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Render stored results as a comparison matrix",
		Long: "report reads one JSON result document (--file) or a lineage directory " +
			"(--results, filtered by --workload/--scale/--engine) and renders the " +
			"matrix: class rollups first, per-query detail after, SKIP/FAIL cells " +
			"with reasons, fidelity footer. --latest keeps only the newest record " +
			"per engine.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var docs []*report.Document
			if inFile != "" {
				doc, err := report.Read(inFile)
				if err != nil {
					return fmt.Errorf("report: %w", err)
				}
				docs = []*report.Document{doc}
			} else {
				var err error
				docs, err = readResults(dir, wlFilter, scale, engineF)
				if err != nil {
					return fmt.Errorf("report: %w", err)
				}
			}
			if latest {
				docs = latestPerEngine(docs)
			}
			if len(docs) == 0 {
				return fmt.Errorf("report: no results matched")
			}
			out := cmd.OutOrStdout()
			if outFile != "" {
				f, err := os.Create(outFile)
				if err != nil {
					return fmt.Errorf("report: create %s: %w", outFile, err)
				}
				defer f.Close()
				out = f
			}
			return renderDocs(out, docs, format)
		},
	}
	f := cmd.Flags()
	f.StringVar(&inFile, "file", "", "single JSON result document to render")
	f.StringVar(&dir, "results", "results", "lineage directory to read")
	f.StringVar(&wlFilter, "workload", "", "filter by workload name")
	f.StringVar(&scale, "scale", "", "filter by scale label")
	f.StringVar(&engineF, "engine", "", "filter by engine name")
	f.BoolVar(&latest, "latest", true, "keep only the newest record per engine")
	f.StringVar(&format, "format", "table", "output format: table|markdown|csv")
	f.StringVar(&outFile, "out", "", "output file (default: stdout)")
	return cmd
}

// renderDocs assembles the matrix and renders it in the chosen format.
func renderDocs(out io.Writer, docs []*report.Document, format string) error {
	m := report.NewMatrix(docs)
	switch format {
	case "table", "":
		report.RenderTable(out, m)
		return nil
	case "markdown", "md":
		report.RenderMarkdown(out, m)
		return nil
	case "csv":
		return report.RenderCSV(out, m)
	default:
		return fmt.Errorf("--format must be table|markdown|csv, got %q", format)
	}
}

// readResults walks a lineage directory and reads every parseable result
// document, applying the given filters. Unparseable files are skipped (the
// lineage may hold v1 records newer readers do not know).
func readResults(dir, wl, scale, eng string) ([]*report.Document, error) {
	var docs []*report.Document
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil //nolint:nilerr // missing subtrees are not fatal to a scan
		}
		doc, rerr := report.Read(path)
		if rerr != nil {
			return nil
		}
		if wl != "" && doc.Workload != wl {
			return nil
		}
		if scale != "" && !strings.EqualFold(doc.Condition.Scale, scale) {
			return nil
		}
		if eng != "" && doc.Condition.Engine != eng {
			return nil
		}
		docs = append(docs, doc)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return docs, nil
}

// latestPerEngine keeps the newest document per engine name, by the
// Condition's StartedAt stamp.
func latestPerEngine(docs []*report.Document) []*report.Document {
	seen := map[string]int{}
	var out []*report.Document
	for _, d := range docs {
		name := d.Condition.Engine
		if idx, ok := seen[name]; !ok {
			seen[name] = len(out)
			out = append(out, d)
		} else if !d.Condition.StartedAt.Before(out[idx].Condition.StartedAt) {
			out[idx] = d
		}
	}
	return out
}
