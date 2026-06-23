package dataset

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/tamnd/graph-bench/target"
)

// paramsFile is the on-disk representation of a dataset's curated parameter
// pools. It lives at params.json beside manifest.json. Each key maps to an
// ordered slice of parameter sets; the slice order is the deterministic curation
// order so two runs on the same dataset use the same draws.
//
// Example (a grid dataset curated for the micro workload):
//
//	{
//	  "micro-khop": [{"seed": "0"}, {"seed": "4"}, {"seed": "8"}],
//	  "micro-sp":   [{"src": "0", "dst": "8"}, {"src": "1", "dst": "5"}]
//	}
//
// The values are raw JSON ([]string or other JSON scalars) mapped to their
// target.Value equivalents by the reader.
type paramsFile map[string][]map[string]any

// readParamsPool reads the params.json file and returns the pool under key, or
// nil when the file does not exist or the key is absent. An existing but
// unreadable file is an error.
func readParamsPool(path, key string) ([]target.Params, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pf paramsFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return nil, err
	}
	raw, ok := pf[key]
	if !ok || len(raw) == 0 {
		return nil, nil
	}
	out := make([]target.Params, len(raw))
	for i, m := range raw {
		p := make(target.Params, len(m))
		for k, v := range m {
			p[k] = jsonValue(v)
		}
		out[i] = p
	}
	return out, nil
}

// WriteParamsPool merges the given pools into params.json at path, creating or
// overwriting the file. Each key in pools is written (or overwritten); keys not
// in pools are preserved from an existing file. This is the writer side curate.go
// calls after computing a pool.
func WriteParamsPool(path string, pools map[string][]target.Params) error {
	var pf paramsFile
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &pf); err != nil {
			return err
		}
	}
	if pf == nil {
		pf = paramsFile{}
	}
	for key, pool := range pools {
		raw := make([]map[string]any, len(pool))
		for i, p := range pool {
			m := make(map[string]any, len(p))
			for k, v := range p {
				m[k] = v
			}
			raw[i] = m
		}
		pf[key] = raw
	}
	b, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// jsonValue converts a JSON-unmarshaled interface{} value to the most specific
// target.Value equivalent: a JSON number that fits int64 becomes int64, a float
// that does not stays float64, a string stays string. JSON objects and arrays
// are kept as-is (map[string]any, []any), since curated parameter values are
// scalars in practice and these forms are not expected in params.json.
func jsonValue(v any) target.Value {
	switch x := v.(type) {
	case float64:
		if i := int64(x); float64(i) == x {
			return i
		}
		return x
	case string:
		return x
	case bool:
		return x
	default:
		return v
	}
}
