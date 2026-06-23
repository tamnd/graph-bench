package workload

import (
	"reflect"
	"testing"

	"github.com/tamnd/graph-bench/target"
)

func TestClassesDerivedAndDeduped(t *testing.T) {
	w := &Workload{
		Name: "x",
		Queries: []*WorkloadQuery{
			{ID: "a", Class: target.Traversal},
			{ID: "b", Class: target.PointRead},
			{ID: "c", Class: target.Traversal}, // duplicate class
			{ID: "d", Class: target.Write},
		},
	}
	got := w.Classes()
	// Classes come back in enum order, deduped.
	want := []target.Class{target.PointRead, target.Traversal, target.Write}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Classes() = %v, want %v", got, want)
	}
}

func TestQueryLookup(t *testing.T) {
	q := &WorkloadQuery{ID: "khop2", Class: target.Traversal}
	w := &Workload{Name: "x", Queries: []*WorkloadQuery{q}}
	got, ok := w.Query("khop2")
	if !ok || got != q {
		t.Errorf("Query(khop2) = %v, %v; want the registered query", got, ok)
	}
	if _, ok := w.Query("missing"); ok {
		t.Error("Query(missing) reported found")
	}
}

func TestResolvePicksDialectAndAttaches(t *testing.T) {
	ref := &target.Answer{Columns: []string{"n"}, Rows: [][]target.Value{{int64(3)}}}
	q := &WorkloadQuery{
		ID:    "khop2",
		Class: target.Traversal,
		Texts: map[Dialect]string{
			Cypher: "MATCH ... RETURN count(*) AS n",
			SQL:    "WITH ... SELECT count(*) AS n",
		},
		Params: NewFixed(target.Params{"seed": int64(7)}),
	}

	tq, params, ok := q.Resolve(Cypher, ref)
	if !ok {
		t.Fatal("Resolve(Cypher) not ok")
	}
	if tq.ID != "khop2" || tq.Class != target.Traversal {
		t.Errorf("resolved id/class = %q/%v", tq.ID, tq.Class)
	}
	if tq.Text != "MATCH ... RETURN count(*) AS n" {
		t.Errorf("resolved text = %q, want the Cypher text", tq.Text)
	}
	if tq.Reference != ref {
		t.Error("resolved query did not carry the reference")
	}
	if params["seed"] != int64(7) {
		t.Errorf("resolved params = %v, want seed=7", params)
	}

	// A dialect with no text is a blank cell, not a failure.
	if _, _, ok := q.Resolve(AGE, ref); ok {
		t.Error("Resolve(AGE) ok, want blank cell (no AGE text)")
	}
}

func TestResolveNilParams(t *testing.T) {
	q := &WorkloadQuery{ID: "scan", Texts: map[Dialect]string{Cypher: "MATCH ..."}}
	_, params, ok := q.Resolve(Cypher, nil)
	if !ok {
		t.Fatal("Resolve not ok")
	}
	if params != nil {
		t.Errorf("params = %v, want nil for a query with no source", params)
	}
}

func TestRegisterLookupAll(t *testing.T) {
	resetRegistry(t)
	a := &Workload{Name: "alpha"}
	b := &Workload{Name: "beta"}
	Register(b)
	Register(a)

	if w, ok := Lookup("alpha"); !ok || w != a {
		t.Errorf("Lookup(alpha) = %v, %v", w, ok)
	}
	if _, ok := Lookup("missing"); ok {
		t.Error("Lookup(missing) reported found")
	}

	all := All()
	if len(all) != 2 || all[0].Name != "alpha" || all[1].Name != "beta" {
		t.Errorf("All() = %v, want [alpha beta] sorted", names(all))
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	resetRegistry(t)
	Register(&Workload{Name: "dup"})
	defer func() {
		if recover() == nil {
			t.Error("duplicate Register did not panic")
		}
	}()
	Register(&Workload{Name: "dup"})
}

func TestRegisterEmptyNamePanics(t *testing.T) {
	resetRegistry(t)
	defer func() {
		if recover() == nil {
			t.Error("Register with empty name did not panic")
		}
	}()
	Register(&Workload{Name: ""})
}

func TestDialectString(t *testing.T) {
	for d, want := range map[Dialect]string{
		Cypher: "cypher", SQLPGQ: "sqlpgq", AGE: "age", SQL: "sql", Dialect(99): "unknown",
	} {
		if got := d.String(); got != want {
			t.Errorf("Dialect(%d).String() = %q, want %q", d, got, want)
		}
	}
}

func TestPoolSourceCyclesAndPool(t *testing.T) {
	sets := []target.Params{{"id": int64(1)}, {"id": int64(2)}, {"id": int64(3)}}
	s := NewPool(sets)
	var got []int64
	for i := 0; i < 5; i++ {
		got = append(got, s.Next()["id"].(int64))
	}
	want := []int64{1, 2, 3, 1, 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Next sequence = %v, want %v (cycles)", got, want)
	}
	if len(s.Pool()) != 3 {
		t.Errorf("Pool() len = %d, want 3", len(s.Pool()))
	}
}

func TestFixedSource(t *testing.T) {
	s := NewFixed(target.Params{"x": int64(9)})
	if s.Next()["x"] != int64(9) || s.Next()["x"] != int64(9) {
		t.Error("Fixed.Next did not return the same set")
	}
	if len(s.Pool()) != 1 {
		t.Errorf("Fixed.Pool() len = %d, want 1", len(s.Pool()))
	}
}

func TestCompareSpecFloatTolerance(t *testing.T) {
	if got := (CompareSpec{}).FloatTolerance(); got != 1e-9 {
		t.Errorf("default FloatTolerance = %g, want 1e-9", got)
	}
	if got := (CompareSpec{FloatTol: 1e-3}).FloatTolerance(); got != 1e-3 {
		t.Errorf("FloatTolerance = %g, want 1e-3", got)
	}
}

// resetRegistry clears the global registry and restores it after the test, so
// registry tests do not interfere with each other or with the real families.
func resetRegistry(t *testing.T) {
	t.Helper()
	saved := registry
	registry = map[string]*Workload{}
	t.Cleanup(func() { registry = saved })
}

func names(ws []*Workload) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Name
	}
	return out
}
