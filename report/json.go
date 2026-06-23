package report

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/tamnd/graph-bench/measure"
)

// JSONRecord is the lossless JSON representation of one engine's measured run.
// Each field maps directly to the measure package types so the JSON carries
// the full condition stamp and every metric. See doc 08 section 4.3.
type JSONRecord struct {
	Engine  string         `json:"engine"`
	Plane   string         `json:"plane"`
	Version string         `json:"version"`
	Result  measure.Result `json:"result"`
}

// RenderJSON writes the per-engine records as a JSON array to w. It is the
// lossless format: every metric, every stamp, nothing summarized away. The
// other three formats (table, markdown, csv) can be re-derived from this JSON.
func RenderJSON(results []EngineResult, w io.Writer) error {
	records := make([]JSONRecord, len(results))
	for i, er := range results {
		records[i] = JSONRecord{
			Engine:  er.Name,
			Plane:   er.Plane,
			Version: er.Version,
			Result:  er.Result,
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(records); err != nil {
		return fmt.Errorf("report: JSON encode: %w", err)
	}
	return nil
}

// ParseJSON reads a JSON array of JSONRecords from r and returns them as
// EngineResult values. Used by the lineage reader and the `report` verb to
// re-render a saved JSON output.
func ParseJSON(r io.Reader) ([]EngineResult, error) {
	var records []JSONRecord
	if err := json.NewDecoder(r).Decode(&records); err != nil {
		return nil, fmt.Errorf("report: JSON decode: %w", err)
	}
	results := make([]EngineResult, len(records))
	for i, rec := range records {
		results[i] = EngineResult{
			Name:    rec.Engine,
			Plane:   rec.Plane,
			Version: rec.Version,
			Result:  rec.Result,
		}
	}
	return results, nil
}
