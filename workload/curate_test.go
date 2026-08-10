package workload

import (
	"context"
	"reflect"
	"strconv"
	"testing"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/dataset/gen"
)

// curateGridDS generates a rows x cols 4-neighbor grid into a temp dir and
// opens it. The grid's directed edges point right and down only, so the
// top-left corner reaches everything and the bottom-right corner is a sink.
func curateGridDS(t *testing.T, rows, cols int) *dataset.Set {
	t.Helper()
	dir := t.TempDir()
	w, err := dataset.NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if _, err := gen.Generate(context.Background(), gen.Config{Kind: "grid", Rows: rows, Cols: cols}, w); err != nil {
		t.Fatalf("Generate grid: %v", err)
	}
	ds, err := dataset.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ds
}

// TestCurateKHopPool checks the degree-banded seed pool: requested size, every
// seed present in the graph, and both a low-degree and a high-degree seed in
// the draw (the point of banding).
func TestCurateKHopPool(t *testing.T) {
	ds := curateGridDS(t, 5, 5)
	pool, err := Curate(ds, "micro-khop", 16, 77)
	if err != nil {
		t.Fatalf("Curate: %v", err)
	}
	if len(pool) != 16 {
		t.Fatalf("khop pool len = %d, want 16", len(pool))
	}
	g, err := LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	minDeg, maxDeg := 99, -1
	for i, p := range pool {
		seed := poolToken(t, p, "seed")
		if !g.HasNode(seed) {
			t.Errorf("pool[%d] seed %q not in graph", i, seed)
		}
		d := g.OutDegree(seed)
		if d < minDeg {
			minDeg = d
		}
		if d > maxDeg {
			maxDeg = d
		}
	}
	if minDeg == maxDeg {
		t.Errorf("all khop seeds have out-degree %d; banding should spread the range", minDeg)
	}
}

// TestCurateSPPool checks the (src, dst) pool: pairs only, both endpoints
// present, and every dst genuinely reachable from its src (curation picks
// from the BFS-reachable set).
func TestCurateSPPool(t *testing.T) {
	ds := curateGridDS(t, 5, 5)
	pool, err := Curate(ds, "micro-sp", 12, 77)
	if err != nil {
		t.Fatalf("Curate: %v", err)
	}
	if len(pool) == 0 {
		t.Fatal("sp pool is empty")
	}
	g, err := LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	for i, p := range pool {
		src := poolToken(t, p, "src")
		dst := poolToken(t, p, "dst")
		if _, ok := g.ShortestPath(src, dst); !ok {
			t.Errorf("pool[%d] pair (%s, %s) is unreachable", i, src, dst)
		}
	}
}

// TestCuratePointPools checks the point pool holds only existing ids and the
// miss pool only absent ones.
func TestCuratePointPools(t *testing.T) {
	ds := curateGridDS(t, 5, 5)
	g, err := LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}

	pointPool, err := Curate(ds, "micro-point", 16, 77)
	if err != nil {
		t.Fatalf("Curate(micro-point): %v", err)
	}
	if len(pointPool) != 16 {
		t.Errorf("point pool len = %d, want 16", len(pointPool))
	}
	for i, p := range pointPool {
		if id := poolToken(t, p, "id"); !g.HasNode(id) {
			t.Errorf("pointPool[%d] id %v is not in the graph", i, p["id"])
		}
	}

	missPool, err := Curate(ds, "micro-point-miss", 16, 77)
	if err != nil {
		t.Fatalf("Curate(micro-point-miss): %v", err)
	}
	if len(missPool) != 16 {
		t.Errorf("miss pool len = %d, want 16", len(missPool))
	}
	for i, p := range missPool {
		id := poolToken(t, p, "id")
		if g.HasNode(id) {
			t.Errorf("missPool[%d] id %q exists; should be a miss", i, id)
		}
	}
}

// TestCurateEdgePool checks the edge pool mixes existing and absent pairs.
func TestCurateEdgePool(t *testing.T) {
	ds := curateGridDS(t, 5, 5)
	pool, err := Curate(ds, "micro-edge", 16, 77)
	if err != nil {
		t.Fatalf("Curate: %v", err)
	}
	if len(pool) != 16 {
		t.Fatalf("edge pool len = %d, want 16", len(pool))
	}
	g, err := LoadGraph(ds)
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	var hits, misses int
	for i, p := range pool {
		src := poolToken(t, p, "src")
		dst := poolToken(t, p, "dst")
		a, aok := g.index[src]
		b, bok := g.index[dst]
		if !aok || !bok {
			t.Fatalf("pool[%d] endpoints (%s, %s) not both in graph", i, src, dst)
		}
		if curateHasEdge(g, a, b) {
			hits++
		} else {
			misses++
		}
	}
	if hits == 0 || misses == 0 {
		t.Errorf("edge pool hits=%d misses=%d; want both > 0", hits, misses)
	}
}

// TestCurateTriangleSentinel checks the parameterless sentinel pool.
func TestCurateTriangleSentinel(t *testing.T) {
	ds := curateGridDS(t, 4, 4)
	pool, err := Curate(ds, "micro-triangle", 16, 77)
	if err != nil {
		t.Fatalf("Curate: %v", err)
	}
	if len(pool) != 1 || len(pool[0]) != 0 {
		t.Errorf("triangle pool = %v, want one empty binding", pool)
	}
}

// TestCurateDeterministic proves the same dataset + seed produce identical
// pools, and a different seed a different draw (on a pool with room to vary).
func TestCurateDeterministic(t *testing.T) {
	ds := curateGridDS(t, 6, 6)
	for _, key := range []string{"micro-khop", "micro-sp", "micro-point", "micro-point-miss", "micro-edge"} {
		a, err := Curate(ds, key, 8, 42)
		if err != nil {
			t.Fatalf("Curate(%s) #1: %v", key, err)
		}
		b, err := Curate(ds, key, 8, 42)
		if err != nil {
			t.Fatalf("Curate(%s) #2: %v", key, err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Errorf("pool %q differs across identical runs:\n%v\n%v", key, a, b)
		}
	}
	a, _ := Curate(ds, "micro-point", 8, 1)
	b, _ := Curate(ds, "micro-point", 8, 2)
	if reflect.DeepEqual(a, b) {
		t.Errorf("micro-point pools identical across different seeds: %v", a)
	}
}

// TestCurateErrors covers the unknown key and bad size paths.
func TestCurateErrors(t *testing.T) {
	ds := curateGridDS(t, 3, 3)
	if _, err := Curate(ds, "no-such-pool", 4, 1); err == nil {
		t.Error("unknown pool key: want error, got nil")
	}
	if _, err := Curate(ds, "micro-point", 0, 1); err == nil {
		t.Error("size 0: want error, got nil")
	}
}

// poolToken returns a curated parameter's id as the string token the oracle
// Graph keys on, requiring the int64 form: the grid fixtures use an integer
// id space, so a string here means curation dropped the type that typed
// engines match on (see IDValue).
func poolToken(t *testing.T, p Params, key string) string {
	t.Helper()
	v, ok := p[key]
	if !ok {
		t.Fatalf("param %q missing from %v", key, p)
	}
	n, ok := v.(int64)
	if !ok {
		t.Fatalf("param %q = %#v, want int64 on an integer id space", key, v)
	}
	return strconv.FormatInt(n, 10)
}
