package finbench

import (
	"context"
	"testing"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

// genFin materializes a small fin dataset (accounts=500) into a temp
// directory and opens it back through the canonical dataset layer.
func genFin(t *testing.T) *dataset.Set {
	t.Helper()
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cfg := gen.Config{Kind: "fin", Seed: 1, Accounts: 500, Days: 8, TxPerDay: 800}
	if _, err := gen.Generate(context.Background(), cfg, w); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ds
}

func TestRegistration(t *testing.T) {
	read, err := workload.Lookup("fb-read")
	if err != nil {
		t.Fatalf("Lookup(fb-read): %v", err)
	}
	write, err := workload.Lookup("fb-write")
	if err != nil {
		t.Fatalf("Lookup(fb-write): %v", err)
	}

	if read.Dataset != "fin-10k" || read.Fidelity != "derived" {
		t.Errorf("fb-read dataset/fidelity = %q/%q, want fin-10k/derived", read.Dataset, read.Fidelity)
	}
	if len(read.Queries) != 10 {
		t.Fatalf("fb-read has %d queries, want 10", len(read.Queries))
	}
	if len(write.Queries) != 2 {
		t.Fatalf("fb-write has %d queries, want 2", len(write.Queries))
	}

	wantClass := map[string]engine.Class{
		"fb-tcr1":  engine.Traversal,
		"fb-tcr2":  engine.Aggregation,
		"fb-tcr3":  engine.Traversal,
		"fb-tcr4":  engine.Subgraph,
		"fb-tcr5":  engine.Aggregation,
		"fb-tcr8":  engine.Traversal,
		"fb-tcr11": engine.Traversal,
		"fb-tcr12": engine.Aggregation,
		"fb-sr1":   engine.PointRead,
		"fb-sr2":   engine.PointRead,
	}
	for id, class := range wantClass {
		q, ok := read.Query(id)
		if !ok {
			t.Errorf("fb-read: query %s missing", id)
			continue
		}
		if q.Class != class {
			t.Errorf("%s class = %s, want %s", id, q.Class, class)
		}
		if q.Texts[engine.Cypher] == "" {
			t.Errorf("%s has empty Cypher text", id)
		}
		if q.Reference == nil || q.Reference.Compute == nil {
			t.Errorf("%s has no reference", id)
		}
		if q.PoolKey != id {
			t.Errorf("%s pool key = %q, want %q", id, q.PoolKey, id)
		}
	}

	for _, id := range []string{"fb-w1", "fb-w2"} {
		q, ok := write.Query(id)
		if !ok {
			t.Fatalf("fb-write: query %s missing", id)
		}
		if q.Class != engine.Write {
			t.Errorf("%s class = %s, want write", id, q.Class)
		}
		if q.Texts[engine.Cypher] == "" || q.PostCondition == "" || q.Teardown == "" {
			t.Errorf("%s must carry text, post-condition, and teardown", id)
		}
		if q.Params == nil {
			t.Errorf("%s has no params", id)
		}
	}
}

func TestPoolsAndReferences(t *testing.T) {
	ds := genFin(t)
	pools, err := BuildPools(ds)
	if err != nil {
		t.Fatalf("BuildPools: %v", err)
	}
	read, err := workload.Lookup("fb-read")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	// Every read query has a non-empty curated pool, and every pool
	// entry computes a reference answer without error.
	answers := map[string][]*workload.Answer{}
	for _, q := range read.Queries {
		pool := pools[q.PoolKey]
		if len(pool) == 0 {
			t.Errorf("%s: empty pool", q.ID)
			continue
		}
		for i, p := range pool {
			ans, err := q.Reference.Compute(ds, p)
			if err != nil {
				t.Errorf("%s[%d]: reference: %v", q.ID, i, err)
				continue
			}
			if ans == nil || len(ans.Columns) == 0 {
				t.Errorf("%s[%d]: degenerate answer", q.ID, i)
				continue
			}
			answers[q.ID] = append(answers[q.ID], ans)
		}
	}

	count0 := func(id string) int64 {
		t.Helper()
		as := answers[id]
		if len(as) == 0 || len(as[0].Rows) == 0 {
			t.Fatalf("%s: no answer rows", id)
		}
		n, ok := as[0].Rows[0][0].(int64)
		if !ok {
			t.Fatalf("%s: first cell is %T, want int64", id, as[0].Rows[0][0])
		}
		return n
	}

	// Non-degeneracy: the curated windows produce work, not empty sets.
	if n := count0("fb-tcr1"); n < 1 {
		t.Errorf("fb-tcr1: reached = %d, want >= 1", n)
	}
	if n := count0("fb-tcr8"); n < 1 {
		t.Errorf("fb-tcr8: reached = %d, want >= 1", n)
	}
	if n := count0("fb-tcr12"); n < 1 {
		t.Errorf("fb-tcr12: accounts = %d, want >= 1", n)
	}
	if n := count0("fb-tcr4"); n < 0 {
		t.Errorf("fb-tcr4: loops = %d, want >= 0", n)
	}
	if rows := answers["fb-tcr2"][0].Rows; len(rows) == 0 {
		t.Error("fb-tcr2: no fan-in breach rows for curated threshold")
	}
	if rows := answers["fb-sr2"][0].Rows; len(rows) == 0 {
		t.Error("fb-sr2: no recent-edge rows for curated account")
	} else {
		last := rows[0][2].(int64)
		for _, r := range rows[1:] {
			ts := r[2].(int64)
			if ts > last {
				t.Errorf("fb-sr2: rows not ts-descending (%d after %d)", ts, last)
			}
			last = ts
		}
	}
	// fb-tcr5 aggregates > 0 for a hub account.
	tcr5 := answers["fb-tcr5"][0].Rows[0]
	inC, outC := tcr5[0].(int64), tcr5[2].(int64)
	if inC+outC <= 0 {
		t.Errorf("fb-tcr5: inCount+outCount = %d, want > 0", inC+outC)
	}
	inS, outS := tcr5[1].(float64), tcr5[3].(float64)
	if inS+outS <= 0 {
		t.Errorf("fb-tcr5: inSum+outSum = %g, want > 0", inS+outS)
	}
	// fb-tcr3 answers a concrete hop count for curated pairs.
	if hops, ok := answers["fb-tcr3"][0].Rows[0][0].(int64); !ok || hops < 1 {
		t.Errorf("fb-tcr3: hops = %v, want int64 >= 1", answers["fb-tcr3"][0].Rows[0][0])
	}
	// fb-sr1 finds its pooled account.
	if len(answers["fb-sr1"][0].Rows) != 1 {
		t.Errorf("fb-sr1: %d rows, want 1", len(answers["fb-sr1"][0].Rows))
	}
}

func TestBind(t *testing.T) {
	ds := genFin(t)
	read, err := workload.Lookup("fb-read")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if err := Bind(read, ds); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	for _, q := range read.Queries {
		if q.Params == nil {
			t.Errorf("%s: no params after Bind", q.ID)
			continue
		}
		first := q.Params.Next()
		if len(first) == 0 {
			t.Errorf("%s: empty param draw", q.ID)
		}
		// Windowed queries carry aligned startTime/endTime.
		if q.PoolKey != "fb-sr1" {
			s, sok := first["startTime"].(int64)
			e, eok := first["endTime"].(int64)
			if !sok || !eok || s >= e {
				t.Errorf("%s: bad window %v..%v", q.ID, first["startTime"], first["endTime"])
			}
		}
	}
}
