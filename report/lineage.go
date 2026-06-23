package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tamnd/graph-bench/measure"
)

// LineageRecord is one append-only record in the results/ tree. It is a
// self-describing, fully-stamped measure.Result for one engine on one run.
// Once written, a record is never edited; a re-run produces a new record next
// to the old one (F9, doc 08 section 5.4).
type LineageRecord struct {
	measure.Result
}

// RecordPath returns the path where this result should be written in the
// lineage tree, given the base directory. The path encodes the workload,
// scale, engine, and timestamp so a reader can find records without parsing
// every file (doc 08 section 5.1):
//
//	<base>/<workload>/<scale>/<timestamp>-<engine>-<plane>-<checksum8>.json
func RecordPath(base string, er EngineResult, t time.Time) string {
	c := er.Result.Condition
	workload := c.Workload
	if workload == "" {
		workload = "unknown"
	}
	scale := c.Scale
	if scale == "" {
		scale = "unknown"
	}
	checksum8 := checksumPrefix8(c.DatasetChecksum)
	stamp := t.UTC().Format("20060102T150405Z")
	name := fmt.Sprintf("%s-%s-%s-%s.json", stamp, slugify(er.Name), slugify(er.Plane), checksum8)
	return filepath.Join(base, workload, scale, name)
}

// Append writes er as a JSON record to the lineage at path. The directory is
// created if it does not exist. Append refuses to write a record whose
// Condition stamp is missing required fields (Engine, Dataset, Workload).
func Append(path string, er EngineResult) error {
	c := er.Result.Condition
	if c.Engine == "" {
		return fmt.Errorf("lineage: Condition.Engine is empty; refusing to append incomplete record")
	}
	if c.Dataset == "" {
		return fmt.Errorf("lineage: Condition.Dataset is empty; refusing to append incomplete record")
	}
	if c.Workload == "" {
		return fmt.Errorf("lineage: Condition.Workload is empty; refusing to append incomplete record")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("lineage: mkdir %s: %w", dir, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("lineage: create %s: %w", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	rec := LineageRecord{er.Result}
	if err := enc.Encode(rec); err != nil {
		return fmt.Errorf("lineage: encode %s: %w", path, err)
	}
	return nil
}

// ReadLineage reads all lineage records under base that match the given
// filters. An empty filter string matches everything. Results are returned in
// lexicographic (chronological) filename order; the caller can further filter
// by timestamp after parsing Condition.Timestamp.
//
// Filters:
//   - workload: the workload subdirectory; "" matches all
//   - scale: the scale subdirectory; "" matches all
//   - engine: matches Condition.Engine; "" matches all
func ReadLineage(base, workload, scale, engine string) ([]EngineResult, error) {
	pattern := filepath.Join(base, coalesce(workload, "*"), coalesce(scale, "*"), "*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("lineage: glob %s: %w", pattern, err)
	}
	var results []EngineResult
	for _, path := range matches {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("lineage: open %s: %w", path, err)
		}
		var rec LineageRecord
		decErr := json.NewDecoder(f).Decode(&rec)
		f.Close()
		if decErr != nil {
			return nil, fmt.Errorf("lineage: decode %s: %w", path, decErr)
		}
		if engine != "" && rec.Condition.Engine != engine {
			continue
		}
		results = append(results, EngineResult{
			Name:    rec.Condition.Engine,
			Plane:   rec.Condition.Plane,
			Version: rec.Condition.EngineVersion,
			Result:  rec.Result,
		})
	}
	return results, nil
}

// checksumPrefix8 extracts the first 8 hex characters of a checksum string.
// Input is expected to be "sha256:<hex>" or bare hex. Returns "00000000" for
// empty or short inputs.
func checksumPrefix8(checksum string) string {
	hex := strings.TrimPrefix(checksum, "sha256:")
	if len(hex) >= 8 {
		return hex[:8]
	}
	if len(hex) > 0 {
		return hex
	}
	return "00000000"
}

// slugify replaces spaces and slashes with hyphens and lower-cases the string
// to produce a safe filename component.
func slugify(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "/", "-")
	return s
}

// coalesce returns the first non-empty value, or the default d.
func coalesce(v, d string) string {
	if v != "" {
		return v
	}
	return d
}
