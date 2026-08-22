package linkbench

import (
	"context"
	"math"
	"testing"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

// genLB materializes a small lb dataset (nodes=500) into a temp
// directory and opens it back through the canonical dataset layer.
func genLB(t *testing.T) *dataset.Set {
	t.Helper()
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, err := gen.Generate(context.Background(), gen.Config{Kind: "lb", Seed: 1, N: 500}, w); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ds
}

func TestRegistration(t *testing.T) {
	w, err := workload.Lookup("linkbench")
	if err != nil {
		t.Fatalf("Lookup(linkbench): %v", err)
	}
	if w.Dataset != "lb-10k" || w.Fidelity != "derived" {
		t.Errorf("dataset/fidelity = %q/%q, want lb-10k/derived", w.Dataset, w.Fidelity)
	}
	if len(w.Queries) != 10 {
		t.Fatalf("linkbench has %d queries, want 10", len(w.Queries))
	}

	wantClass := map[string]engine.Class{
		"lb-get-node":    engine.PointRead,
		"lb-get-link":    engine.PointRead,
		"lb-get-links":   engine.Traversal,
		"lb-count-link":  engine.PointRead,
		"lb-add-node":    engine.Write,
		"lb-update-node": engine.Write,
		"lb-delete-node": engine.Write,
		"lb-add-link":    engine.Write,
		"lb-update-link": engine.Write,
		"lb-delete-link": engine.Write,
	}
	for id, class := range wantClass {
		q, ok := w.Query(id)
		if !ok {
			t.Errorf("query %s missing", id)
			continue
		}
		if q.Class != class {
			t.Errorf("%s class = %s, want %s", id, q.Class, class)
		}
		if q.Texts[engine.Cypher] == "" {
			t.Errorf("%s has empty Cypher text", id)
		}
		if class == engine.Write {
			if q.PostCondition == "" || q.Teardown == "" {
				t.Errorf("%s must carry post-condition and teardown", id)
			}
			if !q.AutocommitOK {
				t.Errorf("%s: AutocommitOK must be true", id)
			}
			if q.Params == nil {
				t.Errorf("%s has no params", id)
			}
		} else if q.Reference == nil || q.Reference.Compute == nil {
			t.Errorf("%s has no reference", id)
		}
	}
}

func TestMix(t *testing.T) {
	w, err := workload.Lookup("linkbench")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if w.Mix == nil {
		t.Fatal("linkbench has no mix")
	}
	if w.Mix.StreamKey != "" {
		t.Errorf("StreamKey = %q, want empty", w.Mix.StreamKey)
	}
	// The published LinkBench distribution, exactly as the spec table
	// states it.
	want := map[string]float64{
		"lb-get-node":    12.9,
		"lb-get-link":    0.5,
		"lb-get-links":   50.7,
		"lb-count-link":  4.9,
		"lb-add-node":    2.6,
		"lb-update-node": 7.4,
		"lb-delete-node": 1.0,
		"lb-add-link":    9.0,
		"lb-update-link": 8.0,
		"lb-delete-link": 3.0,
	}
	var sum float64
	for id, pct := range want {
		got, ok := w.Mix.Weights[id]
		if !ok || got != pct {
			t.Errorf("mix weight %s = %v, want %v", id, got, pct)
		}
		sum += pct
	}
	if len(w.Mix.Weights) != len(want) {
		t.Errorf("mix has %d ops, want %d", len(w.Mix.Weights), len(want))
	}
	if math.Abs(sum-100.0) > 1e-9 {
		t.Errorf("mix weights sum to %v, want 100", sum)
	}
	// Every mix id is a registered query.
	for id := range w.Mix.Weights {
		if _, ok := w.Query(id); !ok {
			t.Errorf("mix names unknown query %s", id)
		}
	}
}

func TestPoolsAndReferences(t *testing.T) {
	ds := genLB(t)
	pools, err := BuildPools(ds)
	if err != nil {
		t.Fatalf("BuildPools: %v", err)
	}
	w, err := workload.Lookup("linkbench")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	answers := map[string][]*workload.Answer{}
	for _, q := range w.Queries {
		if q.Class == engine.Write {
			continue
		}
		pool := pools[q.PoolKey]
		if len(pool) == 0 {
			t.Errorf("%s: empty pool %q", q.ID, q.PoolKey)
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

	// get-node: the pooled hot id exists.
	if rows := answers["lb-get-node"][0].Rows; len(rows) != 1 {
		t.Errorf("lb-get-node: %d rows, want 1", len(rows))
	}
	// get-link: the curated hit exists; the trailing miss entry is 0.
	link := answers["lb-get-link"]
	if n := link[0].Rows[0][0].(int64); n != 1 {
		t.Errorf("lb-get-link hit: found = %d, want 1", n)
	}
	if n := link[len(link)-1].Rows[0][0].(int64); n != 0 {
		t.Errorf("lb-get-link miss: found = %d, want 0", n)
	}
	// get-links: non-empty ordered output for the pooled hot source,
	// times non-increasing.
	list := answers["lb-get-links"][0]
	if len(list.Rows) == 0 {
		t.Fatal("lb-get-links: empty association list for hot source")
	}
	last := list.Rows[0][1].(int64)
	for _, r := range list.Rows[1:] {
		ti := r[1].(int64)
		if ti > last {
			t.Errorf("lb-get-links: times not descending (%d after %d)", ti, last)
		}
		last = ti
	}
	// count-link: positive degree for the hot (src, ltype).
	if n := answers["lb-count-link"][0].Rows[0][0].(int64); n < 1 {
		t.Errorf("lb-count-link: n = %d, want >= 1", n)
	}
	// count-link agrees with the list length (list is uncapped at this
	// scale).
	if n := answers["lb-count-link"][0].Rows[0][0].(int64); n != int64(len(list.Rows)) {
		t.Errorf("lb-count-link = %d but lb-get-links returned %d rows", n, len(list.Rows))
	}
}

// TestPoolsExcludeScratchObject proves no read pool draws the object
// lb-update-node writes to. lb-get-node checks an object's properties
// against the generator's values and the update changes two of them, so
// a pool holding it would turn a write that worked into a verification
// failure, and one failure discards the measurement for the whole
// workload.
func TestPoolsExcludeScratchObject(t *testing.T) {
	pools, err := BuildPools(genLB(t))
	if err != nil {
		t.Fatalf("BuildPools: %v", err)
	}
	for key, pool := range pools {
		if len(pool) == 0 {
			t.Errorf("pool %q is empty", key)
		}
		for i, p := range pool {
			for _, field := range []string{"id", "src", "dst"} {
				v, ok := p[field]
				if !ok {
					continue
				}
				// A numeric id comes through IDValue as an int64.
				if n, ok := v.(int64); ok && n == ScratchObj {
					t.Errorf("pool %q[%d] draws the scratch object as %s", key, i, field)
				}
			}
		}
	}
}

// TestUpdateNodeIsStationary proves the operation puts the object back
// the way it found it: both payloads are the generator's own width, so
// a repetition writes a row the size the one before it wrote, and the
// bracket restores the seed rather than leaving the last update in
// place.
func TestUpdateNodeIsStationary(t *testing.T) {
	if len(scratchSeed) != 64 || len(scratchUpdated) != 64 {
		t.Errorf("payload widths = %d/%d, want the generator's 64 for both",
			len(scratchSeed), len(scratchUpdated))
	}
	q := updateNode()
	for _, d := range []engine.Dialect{engine.Cypher, engine.ZuQL} {
		if q.Before(d) == "" {
			t.Errorf("%s: no setup", d)
		}
		if q.Before(d) != q.After(d) {
			t.Errorf("%s: teardown does not restore what the setup set:\n%s\nvs\n%s",
				d, q.Before(d), q.After(d))
		}
		if q.Check(d) == "" {
			t.Errorf("%s: no post-condition", d)
		}
		if q.Texts[d] == "" {
			t.Errorf("%s: no text", d)
		}
	}
}

func TestBind(t *testing.T) {
	ds := genLB(t)
	w, err := workload.Lookup("linkbench")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if err := Bind(w, ds); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	for _, q := range w.Queries {
		if q.Params == nil {
			t.Errorf("%s: no params after Bind", q.ID)
			continue
		}
		if len(q.Params.Next()) == 0 {
			t.Errorf("%s: empty param draw", q.ID)
		}
	}
}
