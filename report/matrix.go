// Package report turns measured results into a comparison matrix and renders it
// in four output formats (table, JSON, Markdown, CSV). It also owns the
// append-only lineage where published cross-engine numbers live and computes
// the plane-overhead derivation from the gr in-process and gr-bolt rows.
//
// See notes/Spec/2060/bench/08-cli-and-reporting.md section 4 for the full
// matrix design and section 5 for the lineage design.
package report

import (
	"sort"
	"time"

	"github.com/tamnd/graph-bench/measure"
	"github.com/tamnd/graph-bench/target"
)

// Format selects the output rendering.
type Format string

const (
	FormatTable    Format = "table"
	FormatJSON     Format = "json"
	FormatMarkdown Format = "markdown"
	FormatCSV      Format = "csv"
)

// ColumnMode selects whether matrix columns are query classes or query IDs.
type ColumnMode string

const (
	ColumnClass ColumnMode = "class"
	ColumnQuery ColumnMode = "query"
)

// CacheLabel identifies which cache condition a Cell's stats come from.
type CacheLabel string

const (
	CacheWarm CacheLabel = "warm"
	CacheCold CacheLabel = "cold"
)

// Cell is one (engine, class-or-query) entry in the matrix. It carries the
// headline metric (p99 warm by default), the p50, the throughput, and the cold
// p99 when the run measured cold (F5). An empty Metric means the engine did not
// run or could not run that class (Capabilities returned false); the renderer
// shows a blank or "n/a", not a zero.
type Cell struct {
	Metric     time.Duration // p99 (warm) -- the headline
	P50        time.Duration // p50 (warm)
	Throughput float64       // queries/second at the measured concurrency
	Cold       time.Duration // p99 cold; zero when not measured (F5)
	Empty      bool          // true when the engine did not run this class/query
}

// Row is one engine in the matrix. Name is the engine's registered name,
// Plane is its network plane ("inproc", "bolt", "native"), and Cells is keyed
// by column label (class name or query id). Version is the live-queried engine
// version from the condition stamp.
type Row struct {
	Name    string
	Plane   string
	Version string
	Cells   map[string]Cell
}

// Matrix is the full comparison grid: a column order and the engine rows. Rows
// are grouped by plane with in-process engines first per the spec (F3, section
// 4.4 of doc 08). Within a plane the rows preserve the run order.
type Matrix struct {
	Columns []string // column labels in order (class names or query IDs)
	Rows    []*Row
	// RunConditions is a short human-readable summary of the shared run
	// conditions (dataset, scale, workload, seed), printed in table and
	// markdown headers.
	RunConditions string
}

// EngineResult pairs a named, planed engine with its measured result.
type EngineResult struct {
	Name    string
	Plane   string
	Version string
	Result  measure.Result
}

// Assemble builds a Matrix from a set of per-engine results. mode selects
// whether columns are query classes or individual query IDs; when mode is
// ColumnClass, the columns are derived from all classes seen across all
// engines, in the canonical order (PointRead, Traversal, Subgraph, Write,
// Analytical). When mode is ColumnQuery, columns are all query IDs seen across
// all ByQuery maps.
//
// Rows are ordered with in-process engines first (plane == "inproc"), then Bolt
// engines (plane == "bolt"), then native engines (plane == "native"), and within
// each plane in the order the engines appear in results.
func Assemble(results []EngineResult, mode ColumnMode, runConditions string) *Matrix {
	m := &Matrix{RunConditions: runConditions}

	// Build the column set.
	if mode == ColumnClass {
		// Canonical class order.
		canonical := []target.Class{
			target.PointRead, target.Traversal, target.Subgraph,
			target.Write, target.Analytical,
		}
		seen := map[target.Class]bool{}
		for _, er := range results {
			for cl := range er.Result.Stats {
				seen[cl] = true
			}
			if er.Result.Cold != nil {
				for cl := range er.Result.Cold {
					seen[cl] = true
				}
			}
		}
		for _, cl := range canonical {
			if seen[cl] {
				// Use PascalCase column names for readability in table headers.
				m.Columns = append(m.Columns, classLabel(cl))
			}
		}
	} else {
		// Query ID mode: collect all query IDs seen, sort alphabetically.
		seen := map[string]bool{}
		for _, er := range results {
			for qid := range er.Result.ByQuery {
				seen[qid] = true
			}
		}
		for qid := range seen {
			m.Columns = append(m.Columns, qid)
		}
		sort.Strings(m.Columns)
	}

	// Build rows, ordered inproc < bolt < native < (other).
	planeOrder := map[string]int{"inproc": 0, "bolt": 1, "native": 2}
	ordered := make([]EngineResult, len(results))
	copy(ordered, results)
	sort.SliceStable(ordered, func(i, j int) bool {
		pi := planeOrder[ordered[i].Plane]
		pj := planeOrder[ordered[j].Plane]
		if pi != pj {
			return pi < pj
		}
		return false // preserve original order within the same plane
	})

	for _, er := range ordered {
		row := &Row{
			Name:    er.Name,
			Plane:   er.Plane,
			Version: er.Version,
			Cells:   make(map[string]Cell, len(m.Columns)),
		}
		for _, col := range m.Columns {
			var c Cell
			if mode == ColumnClass {
				// Parse class name back to class constant.
				cl := parseClass(col)
				warmStat, hasWarm := er.Result.Stats[cl]
				coldStat, hasCold := er.Result.Cold[cl]
				if !hasWarm && !hasCold {
					c.Empty = true
				} else {
					c.Metric = warmStat.P99
					c.P50 = warmStat.P50
					c.Throughput = warmStat.Throughput
					if hasCold {
						c.Cold = coldStat.P99
					}
				}
			} else {
				warmStat, hasWarm := er.Result.ByQuery[col]
				if !hasWarm {
					c.Empty = true
				} else {
					c.Metric = warmStat.P99
					c.P50 = warmStat.P50
					c.Throughput = warmStat.Throughput
				}
			}
			row.Cells[col] = c
		}
		m.Rows = append(m.Rows, row)
	}
	return m
}

// PlaneOverhead computes the Bolt overhead for each class by subtracting the
// in-process row's latency from the Bolt row's latency. Both rows must share
// the same engine base name (e.g., "gr" and "gr-bolt"). Returns nil if either
// row is not found in the matrix.
func (m *Matrix) PlaneOverhead(inprocName, boltName string) *PlaneOverheadReport {
	var inprocRow, boltRow *Row
	for _, r := range m.Rows {
		if r.Name == inprocName {
			inprocRow = r
		}
		if r.Name == boltName {
			boltRow = r
		}
	}
	if inprocRow == nil || boltRow == nil {
		return nil
	}
	rep := &PlaneOverheadReport{
		InprocEngine: inprocName,
		BoltEngine:   boltName,
		Overhead:     make(map[string]Overhead, len(m.Columns)),
	}
	for _, col := range m.Columns {
		ic := inprocRow.Cells[col]
		bc := boltRow.Cells[col]
		if ic.Empty || bc.Empty || ic.Metric == 0 {
			continue
		}
		abs := bc.Metric - ic.Metric
		rel := float64(bc.Metric) / float64(ic.Metric)
		rep.Overhead[col] = Overhead{Absolute: abs, Relative: rel}
	}
	return rep
}

// PlaneOverheadReport is the derivation of Bolt protocol overhead from two
// engine rows (in-process and Bolt of the same engine). See doc 08 section 7.
type PlaneOverheadReport struct {
	InprocEngine string
	BoltEngine   string
	Overhead     map[string]Overhead // keyed by column label
}

// Overhead is the absolute and relative Bolt overhead for one column.
// Absolute = bolt.P99 - inproc.P99; Relative = bolt.P99 / inproc.P99.
// A relative overhead of 2.0 means Bolt adds as much latency as the engine
// itself (the protocol cost equals the compute cost).
type Overhead struct {
	Absolute time.Duration // bolt p99 minus inproc p99
	Relative float64       // bolt p99 / inproc p99 (1.0 = no overhead)
}

// classLabel returns the PascalCase column label for a class, used in matrix
// column headers. The target package's Class.String() is lowercase-dash
// ("point-read"), which is good for flags and JSON keys but less readable in
// a table header.
func classLabel(cl target.Class) string {
	switch cl {
	case target.PointRead:
		return "PointRead"
	case target.Traversal:
		return "Traversal"
	case target.Subgraph:
		return "Subgraph"
	case target.Write:
		return "Write"
	case target.Analytical:
		return "Analytical"
	default:
		return cl.String()
	}
}

// parseClass maps a PascalCase column label back to the Class constant.
func parseClass(name string) target.Class {
	switch name {
	case "PointRead":
		return target.PointRead
	case "Traversal":
		return target.Traversal
	case "Subgraph":
		return target.Subgraph
	case "Write":
		return target.Write
	case "Analytical":
		return target.Analytical
	default:
		return 0
	}
}
