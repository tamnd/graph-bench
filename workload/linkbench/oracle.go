package linkbench

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"sync"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

// lbObj is one Obj row from the canonical CSV.
type lbObj struct {
	otype   int64
	version int64
	time    int64
	payload string
}

// lbLink is one LINK edge as seen from its source.
type lbLink struct {
	dst     string
	ltype   int64
	time    int64
	payload string
}

// lbGraph is the typed in-memory view the references compute over: the
// object table and the src -> []{dst, ltype, time, payload} association
// adjacency. The generic workload.Graph merges relationship types and
// drops properties, so this package loads its own typed adjacency
// (straight-line Go over the CSVs, ADR-9).
type lbGraph struct {
	objs map[string]lbObj
	ids  []string // file order (numeric ascending as generated)
	out  map[string][]lbLink
}

var (
	lbMu    sync.Mutex
	lbCache = map[string]*lbGraph{}
)

// lbFor loads (or returns the cached) typed graph for a dataset.
func lbFor(ds engine.Dataset) (*lbGraph, error) {
	key := ds.Dir() + "|" + ds.Checksum()
	lbMu.Lock()
	defer lbMu.Unlock()
	if g, ok := lbCache[key]; ok {
		return g, nil
	}
	g, err := loadLB(ds)
	if err != nil {
		return nil, err
	}
	lbCache[key] = g
	return g, nil
}

// loadLB reads Obj and LINK from the canonical CSVs.
func loadLB(ds engine.Dataset) (*lbGraph, error) {
	g := &lbGraph{objs: map[string]lbObj{}, out: map[string][]lbLink{}}

	nodeFiles, err := ds.NodeFiles("Obj")
	if err != nil {
		return nil, fmt.Errorf("linkbench: %w", err)
	}
	for _, f := range nodeFiles {
		cols, err := dataset.ReadHeader(f)
		if err != nil {
			return nil, err
		}
		idIdx := colType(cols, "ID")
		otIdx := colName(cols, "otype")
		veIdx := colName(cols, "version")
		tiIdx := colName(cols, "time")
		paIdx := colName(cols, "payload")
		if idIdx < 0 || otIdx < 0 || veIdx < 0 || tiIdx < 0 || paIdx < 0 {
			return nil, fmt.Errorf("linkbench: %s lacks Obj columns", f)
		}
		err = lbScanRows(f, func(rec []string) error {
			ot, err1 := strconv.ParseInt(rec[otIdx], 10, 64)
			ve, err2 := strconv.ParseInt(rec[veIdx], 10, 64)
			ti, err3 := strconv.ParseInt(rec[tiIdx], 10, 64)
			if err1 != nil || err2 != nil || err3 != nil {
				return fmt.Errorf("linkbench: bad Obj row in %s", f)
			}
			id := rec[idIdx]
			g.objs[id] = lbObj{otype: ot, version: ve, time: ti, payload: rec[paIdx]}
			g.ids = append(g.ids, id)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	relFiles, err := ds.RelFiles("LINK")
	if err != nil {
		return nil, fmt.Errorf("linkbench: %w", err)
	}
	for _, f := range relFiles {
		cols, err := dataset.ReadHeader(f)
		if err != nil {
			return nil, err
		}
		sIdx := colType(cols, "START_ID")
		eIdx := colType(cols, "END_ID")
		ltIdx := colName(cols, "ltype")
		tiIdx := colName(cols, "time")
		paIdx := colName(cols, "payload")
		if sIdx < 0 || eIdx < 0 || ltIdx < 0 || tiIdx < 0 || paIdx < 0 {
			return nil, fmt.Errorf("linkbench: %s lacks LINK columns", f)
		}
		err = lbScanRows(f, func(rec []string) error {
			lt, err1 := strconv.ParseInt(rec[ltIdx], 10, 64)
			ti, err2 := strconv.ParseInt(rec[tiIdx], 10, 64)
			if err1 != nil || err2 != nil {
				return fmt.Errorf("linkbench: bad LINK row in %s", f)
			}
			src := rec[sIdx]
			g.out[src] = append(g.out[src], lbLink{
				dst: rec[eIdx], ltype: lt, time: ti, payload: rec[paIdx],
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return g, nil
}

// linksList returns the (src, ltype) association list as
// [dst, time, payload] rows, newest first (time descending, payload
// ascending — the same total order lb-get-links' ORDER BY fixes), up to
// limit rows.
func (g *lbGraph) linksList(src string, ltype int64, limit int) [][]engine.Value {
	var links []lbLink
	for _, l := range g.out[src] {
		if l.ltype == ltype {
			links = append(links, l)
		}
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].time != links[j].time {
			return links[i].time > links[j].time
		}
		return links[i].payload < links[j].payload
	})
	if len(links) > limit {
		links = links[:limit]
	}
	rows := make([][]engine.Value, 0, len(links))
	for _, l := range links {
		rows = append(rows, []engine.Value{workload.IDValue(l.dst), l.time, l.payload})
	}
	return rows
}

// lbScanRows reads a canonical CSV file, skipping the header, calling
// fn per data row. The record slice is reused; fn must not retain it.
func lbScanRows(path string, fn func(rec []string) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.ReuseRecord = true
	first := true
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("linkbench: read %s: %w", path, err)
		}
		if first {
			first = false
			continue
		}
		if err := fn(rec); err != nil {
			return err
		}
	}
}

// colType returns the index of the column with the structural type, or -1.
func colType(cols []engine.Column, typ string) int {
	for i, c := range cols {
		if c.Type == typ {
			return i
		}
	}
	return -1
}

// colName returns the index of the property column with the name, or -1.
func colName(cols []engine.Column, name string) int {
	for i, c := range cols {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// lpStr returns an id param as the raw CSV token the graph's maps key on,
// accepting either spelling. The pools bind an integer id as int64 so the
// engines match their integer id property (workload.IDValue).
func lpStr(p workload.Params, key string) (string, error) {
	if _, ok := p[key]; !ok {
		return "", fmt.Errorf("linkbench: param %q missing", key)
	}
	s, ok := workload.Token(p, key)
	if !ok {
		return "", fmt.Errorf("linkbench: param %q is %T, want an id", key, p[key])
	}
	return s, nil
}

// lpInt extracts an integer param, normalizing numeric spellings.
func lpInt(p workload.Params, key string) (int64, error) {
	v, ok := p[key]
	if !ok {
		return 0, fmt.Errorf("linkbench: param %q missing", key)
	}
	switch n := v.(type) {
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	case float64:
		return int64(n), nil
	}
	return 0, fmt.Errorf("linkbench: param %q is %T, want integer", key, v)
}

// srcLtype extracts the (src, ltype) pair the association reads share.
func srcLtype(p workload.Params) (string, int64, error) {
	src, err := lpStr(p, "src")
	if err != nil {
		return "", 0, err
	}
	lt, err := lpInt(p, "ltype")
	if err != nil {
		return "", 0, err
	}
	return src, lt, nil
}
