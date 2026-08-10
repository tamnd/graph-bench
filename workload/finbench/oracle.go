package finbench

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

// hubCap is FinBench's hub-truncation bound: every traversal expansion
// follows at most this many most-recent in-window edges out of one node
// (spec 06 §4 "hub truncation"). The reference implements the cap
// identically to what the query texts ask: at fin-10k scale a windowed
// expansion stays far below 5000 edges, so the cap never binds and the
// untruncated Cypher text asks the same question the capped reference
// answers. A dataset whose hubs exceed the cap in-window needs dialect
// texts that carry an explicit per-expansion LIMIT before these
// references remain faithful.
const hubCap = 5000

// finAccount is one Account row from the canonical CSV.
type finAccount struct {
	id         string
	createTime int64
	blocked    bool
}

// finEdge is one money-movement edge as seen from one endpoint. uid is
// the file-global ordinal that stands in for relationship identity, so
// the reference can honor Cypher's relationship-uniqueness (trail)
// semantics where it matters (fb-tcr11's explicit 3-hop chain).
type finEdge struct {
	uid    int
	other  string // the far endpoint (dst for out lists, src for in lists)
	amount float64
	ts     int64
}

// finGraph is the typed in-memory view of the fin dataset the references
// compute over: Account nodes plus the TRANSFER adjacency (both
// directions, sorted newest-first so the hub cap keeps the most recent
// edges) and the Loan DEPOSIT edges fb-tcr8 chases. The generic
// workload.Graph merges relationship types, so this package loads its
// own typed adjacency instead (straight-line Go over the CSVs, ADR-9).
type finGraph struct {
	accounts []finAccount // file order (deterministic)
	acc      map[string]finAccount
	out      map[string][]finEdge // TRANSFER, src -> edges, ts descending
	in       map[string][]finEdge // TRANSFER, dst -> edges, ts descending
	deposits map[string][]finEdge // DEPOSIT, loan -> edges to accounts
	loanIDs  []string             // sorted
	minTs    int64                // TRANSFER ts range, for window curation
	maxTs    int64
}

var (
	finMu    sync.Mutex
	finCache = map[string]*finGraph{}
)

// finFor loads (or returns the cached) typed graph for a dataset. The
// cache is keyed by directory and checksum so repeated reference
// computations over one materialized dataset parse the CSVs once.
func finFor(ds engine.Dataset) (*finGraph, error) {
	key := ds.Dir() + "|" + ds.Checksum()
	finMu.Lock()
	defer finMu.Unlock()
	if g, ok := finCache[key]; ok {
		return g, nil
	}
	g, err := loadFin(ds)
	if err != nil {
		return nil, err
	}
	finCache[key] = g
	return g, nil
}

// loadFin reads Account, TRANSFER, and DEPOSIT from the canonical CSVs.
func loadFin(ds engine.Dataset) (*finGraph, error) {
	g := &finGraph{
		acc:      map[string]finAccount{},
		out:      map[string][]finEdge{},
		in:       map[string][]finEdge{},
		deposits: map[string][]finEdge{},
	}

	nodeFiles, err := ds.NodeFiles("Account")
	if err != nil {
		return nil, fmt.Errorf("finbench: %w", err)
	}
	for _, f := range nodeFiles {
		cols, err := dataset.ReadHeader(f)
		if err != nil {
			return nil, err
		}
		idIdx := typeIndex(cols, "ID")
		ctIdx := nameIndex(cols, "createTime")
		blIdx := nameIndex(cols, "isBlocked")
		if idIdx < 0 || ctIdx < 0 || blIdx < 0 {
			return nil, fmt.Errorf("finbench: %s lacks id/createTime/isBlocked columns", f)
		}
		err = scanRows(f, func(rec []string) error {
			ct, err := strconv.ParseInt(rec[ctIdx], 10, 64)
			if err != nil {
				return fmt.Errorf("finbench: bad createTime in %s: %w", f, err)
			}
			bl, err := strconv.ParseBool(rec[blIdx])
			if err != nil {
				return fmt.Errorf("finbench: bad isBlocked in %s: %w", f, err)
			}
			a := finAccount{id: rec[idIdx], createTime: ct, blocked: bl}
			g.accounts = append(g.accounts, a)
			g.acc[a.id] = a
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	uid := 0
	first := true
	err = scanMoneyRels(ds, "TRANSFER", func(src, dst string, amount float64, ts int64) {
		g.out[src] = append(g.out[src], finEdge{uid: uid, other: dst, amount: amount, ts: ts})
		g.in[dst] = append(g.in[dst], finEdge{uid: uid, other: src, amount: amount, ts: ts})
		uid++
		if first || ts < g.minTs {
			g.minTs = ts
		}
		if first || ts > g.maxTs {
			g.maxTs = ts
		}
		first = false
	})
	if err != nil {
		return nil, err
	}
	if first {
		return nil, fmt.Errorf("finbench: dataset %s has no TRANSFER edges", ds.Name())
	}

	err = scanMoneyRels(ds, "DEPOSIT", func(src, dst string, amount float64, ts int64) {
		g.deposits[src] = append(g.deposits[src], finEdge{uid: uid, other: dst, amount: amount, ts: ts})
		uid++
	})
	if err != nil {
		return nil, err
	}
	for l := range g.deposits {
		g.loanIDs = append(g.loanIDs, l)
	}
	sort.Strings(g.loanIDs)

	byTsDesc := func(edges []finEdge) {
		sort.Slice(edges, func(i, j int) bool {
			if edges[i].ts != edges[j].ts {
				return edges[i].ts > edges[j].ts
			}
			return edges[i].uid < edges[j].uid
		})
	}
	for k := range g.out {
		byTsDesc(g.out[k])
	}
	for k := range g.in {
		byTsDesc(g.in[k])
	}
	return g, nil
}

// scanMoneyRels reads one money-edge relationship type (START_ID,
// END_ID, amount, ts) and calls fn per edge in file order.
func scanMoneyRels(ds engine.Dataset, typ string, fn func(src, dst string, amount float64, ts int64)) error {
	files, err := ds.RelFiles(typ)
	if err != nil {
		return fmt.Errorf("finbench: %w", err)
	}
	for _, f := range files {
		cols, err := dataset.ReadHeader(f)
		if err != nil {
			return err
		}
		sIdx := typeIndex(cols, "START_ID")
		eIdx := typeIndex(cols, "END_ID")
		amIdx := nameIndex(cols, "amount")
		tsIdx := nameIndex(cols, "ts")
		if sIdx < 0 || eIdx < 0 || amIdx < 0 || tsIdx < 0 {
			return fmt.Errorf("finbench: %s lacks endpoint/amount/ts columns", f)
		}
		err = scanRows(f, func(rec []string) error {
			am, err := strconv.ParseFloat(rec[amIdx], 64)
			if err != nil {
				return fmt.Errorf("finbench: bad amount in %s: %w", f, err)
			}
			ts, err := strconv.ParseInt(rec[tsIdx], 10, 64)
			if err != nil {
				return fmt.Errorf("finbench: bad ts in %s: %w", f, err)
			}
			fn(rec[sIdx], rec[eIdx], am, ts)
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// scanRows reads a canonical CSV file, skipping the header, calling fn
// per data row. The record slice is reused; fn must not retain it.
func scanRows(path string, fn func(rec []string) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.ReuseRecord = true
	firstRec := true
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("finbench: read %s: %w", path, err)
		}
		if firstRec {
			firstRec = false
			continue
		}
		if err := fn(rec); err != nil {
			return err
		}
	}
}

// typeIndex returns the index of the column with the structural type
// (ID, START_ID, END_ID), or -1.
func typeIndex(cols []engine.Column, typ string) int {
	for i, c := range cols {
		if c.Type == typ {
			return i
		}
	}
	return -1
}

// nameIndex returns the index of the property column with the name, or -1.
func nameIndex(cols []engine.Column, name string) int {
	for i, c := range cols {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// winEdges filters a ts-descending edge list to the window [s, e),
// keeping at most limit edges (the most recent, because the input is
// ts-descending); limit <= 0 means unbounded. Traversal expansions pass
// hubCap; aggregation references pass 0 because the spec's cap applies
// to traversal texts only.
func winEdges(edges []finEdge, s, e int64, limit int) []finEdge {
	var out []finEdge
	for _, ed := range edges {
		if ed.ts < s || ed.ts >= e {
			continue
		}
		out = append(out, ed)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

// khopOut returns the distinct endpoints of windowed TRANSFER walks of
// length 1..depth from seed (frontier expansion, hub-capped per node).
// A walk of length <= k implies a simple path of length <= k, so the
// frontier union equals Cypher's count(DISTINCT b) over *1..depth under
// trail semantics. The seed itself is included when a cycle returns.
func (g *finGraph) khopOut(seed string, s, e int64, depth int) map[string]bool {
	reached := map[string]bool{}
	frontier := []string{seed}
	for d := 0; d < depth; d++ {
		next := map[string]bool{}
		for _, u := range frontier {
			for _, ed := range winEdges(g.out[u], s, e, hubCap) {
				next[ed.other] = true
			}
		}
		nf := make([]string, 0, len(next))
		for v := range next {
			reached[v] = true
			nf = append(nf, v)
		}
		sort.Strings(nf)
		frontier = nf
	}
	return reached
}

// shortestWin returns the minimum windowed hop count from src to dst
// (1..maxHops), ok=false when dst is not reachable within the bound.
func (g *finGraph) shortestWin(src, dst string, s, e int64, maxHops int) (int64, bool) {
	visited := map[string]bool{src: true}
	frontier := []string{src}
	for hop := 1; hop <= maxHops; hop++ {
		next := map[string]bool{}
		for _, u := range frontier {
			for _, ed := range winEdges(g.out[u], s, e, hubCap) {
				if ed.other == dst {
					return int64(hop), true
				}
				if !visited[ed.other] {
					next[ed.other] = true
				}
			}
		}
		nf := make([]string, 0, len(next))
		for v := range next {
			visited[v] = true
			nf = append(nf, v)
		}
		sort.Strings(nf)
		frontier = nf
		if len(frontier) == 0 {
			break
		}
	}
	return 0, false
}

// edgeWinTo reports whether a windowed TRANSFER edge m->a exists.
func (g *finGraph) edgeWinTo(m, a string, s, e int64) bool {
	for _, ed := range g.out[m] {
		if ed.ts >= s && ed.ts < e && ed.other == a {
			return true
		}
	}
	return false
}

// loops counts the distinct accounts m != a on a windowed money loop
// a -TRANSFER*1..3-> m -TRANSFER-> a (fb-tcr4).
func (g *finGraph) loops(id string, s, e int64) int64 {
	var n int64
	for m := range g.khopOut(id, s, e, 3) {
		if m == id {
			continue
		}
		if g.edgeWinTo(m, id, s, e) {
			n++
		}
	}
	return n
}

// fanIn returns [id, sources] rows for accounts receiving windowed
// transfers from more than threshold distinct sources (fb-tcr2).
func (g *finGraph) fanIn(s, e, threshold int64) [][]engine.Value {
	var rows [][]engine.Value
	for _, a := range g.accounts {
		srcs := map[string]bool{}
		for _, ed := range winEdges(g.in[a.id], s, e, 0) {
			srcs[ed.other] = true
		}
		if int64(len(srcs)) > threshold {
			rows = append(rows, []engine.Value{workload.IDValue(a.id), int64(len(srcs))})
		}
	}
	return rows
}

// fanStats returns the windowed in/out transfer counts and amount sums
// of one account (fb-tcr5).
func (g *finGraph) fanStats(id string, s, e int64) (inCount int64, inSum float64, outCount int64, outSum float64) {
	for _, ed := range winEdges(g.in[id], s, e, 0) {
		inCount++
		inSum += ed.amount
	}
	for _, ed := range winEdges(g.out[id], s, e, 0) {
		outCount++
		outSum += ed.amount
	}
	return
}

// loanChain counts the distinct accounts reached by following a loan's
// windowed DEPOSIT edge and then 1..2 windowed transfers (fb-tcr8).
func (g *finGraph) loanChain(loan string, s, e int64) int64 {
	reached := map[string]bool{}
	for _, d := range g.deposits[loan] {
		if d.ts < s || d.ts >= e {
			continue
		}
		for v := range g.khopOut(d.other, s, e, 2) {
			reached[v] = true
		}
	}
	return int64(len(reached))
}

// decayReach counts the distinct endpoints of windowed 3-hop transfer
// chains from id where each hop's amount is at most decay times the
// previous hop's (fb-tcr11). The uid check honors Cypher's relationship
// uniqueness: the third hop may not reuse the first hop's edge (the only
// coincidence possible in a 3-hop chain without self-loops).
func (g *finGraph) decayReach(id string, s, e int64, decay float64) int64 {
	reached := map[string]bool{}
	for _, e1 := range winEdges(g.out[id], s, e, hubCap) {
		for _, e2 := range winEdges(g.out[e1.other], s, e, hubCap) {
			if e2.amount > e1.amount*decay {
				continue
			}
			for _, e3 := range winEdges(g.out[e2.other], s, e, hubCap) {
				if e3.amount > e2.amount*decay || e3.uid == e1.uid {
					continue
				}
				reached[e3.other] = true
			}
		}
	}
	return int64(len(reached))
}

// newAccountBurst counts accounts created inside the window that made
// more than threshold windowed outgoing transfers (fb-tcr12).
func (g *finGraph) newAccountBurst(s, e, threshold int64) int64 {
	var n int64
	for _, a := range g.accounts {
		if a.createTime < s || a.createTime >= e {
			continue
		}
		if int64(len(winEdges(g.out[a.id], s, e, 0))) > threshold {
			n++
		}
	}
	return n
}

// recentOut returns up to limit windowed outgoing transfers of an
// account as [dst, amount, ts] rows, newest first with a total order
// tie-break matching fb-sr2's ORDER BY.
func (g *finGraph) recentOut(id string, s, e int64, limit int) [][]engine.Value {
	edges := winEdges(g.out[id], s, e, 0)
	sort.Slice(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.ts != b.ts {
			return a.ts > b.ts
		}
		if a.amount != b.amount {
			return a.amount > b.amount
		}
		if a.other != b.other {
			return a.other < b.other
		}
		return a.uid < b.uid
	})
	if len(edges) > limit {
		edges = edges[:limit]
	}
	rows := make([][]engine.Value, 0, len(edges))
	for _, ed := range edges {
		rows = append(rows, []engine.Value{workload.IDValue(ed.other), ed.amount, ed.ts})
	}
	return rows
}

// Param extraction helpers: pools store engine.Value, so the accessors
// normalize the numeric spellings a pool or JSON round-trip may use.

// pStr returns an id parameter as the raw CSV token the graph's maps key on,
// accepting either spelling. The pools bind an integer id as int64 so the
// engines match their integer id property (workload.IDValue); insisting on a
// Go string here would reject exactly that typing.
func pStr(p workload.Params, key string) (string, error) {
	if _, ok := p[key]; !ok {
		return "", fmt.Errorf("finbench: param %q missing", key)
	}
	s, ok := workload.Token(p, key)
	if !ok {
		return "", fmt.Errorf("finbench: param %q is %T, want an id", key, p[key])
	}
	return s, nil
}

func pInt(p workload.Params, key string) (int64, error) {
	v, ok := p[key]
	if !ok {
		return 0, fmt.Errorf("finbench: param %q missing", key)
	}
	switch n := v.(type) {
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	case float64:
		return int64(n), nil
	}
	return 0, fmt.Errorf("finbench: param %q is %T, want integer", key, v)
}

func pFloat(p workload.Params, key string) (float64, error) {
	v, ok := p[key]
	if !ok {
		return 0, fmt.Errorf("finbench: param %q missing", key)
	}
	switch n := v.(type) {
	case float64:
		return n, nil
	case int64:
		return float64(n), nil
	case int:
		return float64(n), nil
	}
	return 0, fmt.Errorf("finbench: param %q is %T, want float", key, v)
}

// window extracts the startTime/endTime pair every windowed query takes.
func window(p workload.Params) (s, e int64, err error) {
	if s, err = pInt(p, "startTime"); err != nil {
		return
	}
	e, err = pInt(p, "endTime")
	return
}
